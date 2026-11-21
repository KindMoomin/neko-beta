# Build guide: the `Workload` object (first cut)

Audience: a developer new to this repo. Follow the steps in order. Each step ends
with a check you can run. Don't skip the checks — they catch mistakes early.

## What you're building

A new top-level kind, **`Workload`**, that collapses the `Pod` + `Deployment` +
`ReplicaSet` trinity into one object you declare. `replicas>=1` always (there is
no bare Pod to author). A new controller **lowers** a `Workload` into a
`ReplicaSet`; the existing ReplicaSet→Pod→binder→litterbox machinery (already
working) runs the pods. Nothing about the current Deployment/ReplicaSet/Pod path
changes — `Workload` is added *alongside* them.

Scope of THIS cut (decided in `docs/VOCABULARY.md`, "Fork A1 + B1"):
- **Lowering only.** `Workload` → `ReplicaSet`. No auto-observability (the per-pod
  RTT join is a known-broken netns gap — see `docs/PROGRESS.md`). No config/secret
  reload (those kinds don't exist yet).
- **Coexist, don't replace.** Leave `Deployment` and `examples/deployment.toml`
  untouched.

By the end: `neko apply examples/workload.toml` (replicas=2) → two pods reach
`Running`, exactly like the Deployment path, but from a single `Workload` object.

## Background: the pattern you're copying

A `Workload` is structurally identical to a `Deployment` here — it owns a replica
count, a selector, and a pod template, and it lowers to a `ReplicaSet`. So you are
**copying the `Deployment` implementation and renaming it**. Read these two files
first; you will mirror them exactly:
- `internal/api/api.go` — the `Deployment` type (lines ~104–125) and its
  `DeepCopyObject`.
- `internal/controller/deployment/deployment.go` — the whole reconciler. Your
  Workload reconciler is this file with `Deployment`→`Workload`.

Key facts about how kinds work here:
- Every kind is a plain struct implementing `api.Object` (`GetMeta`, `GetType`,
  `DeepCopyObject`). Deep copy must not alias maps/slices — that's why
  `DeepCopyObject` calls `copyMeta`/`copyStringMap`/`copyPodSpec`
  (`internal/api/deepcopy.go`).
- `api.New(kind)` (`internal/api/registry.go`) is the ONE place mapping a kind
  string to a type. If a kind isn't in that switch, the store/WAL/httpapi reject
  it. You MUST add `Workload` there.
- A controller implements `controller.Controller`
  (`internal/controller/controller.go`): `Name()`, `Kinds()`,
  `MapToReconcileKey()`, `Reconcile()`. It's registered in
  `cmd/neko/main.go` in the `controller.Run(...)` call.
- Reconcilers are **level-triggered**: read desired (the Workload) + actual (its
  ReplicaSet) each pass and converge with `Client.Apply` (optimistic concurrency).
  Don't track state between calls.

---

## Step 1 — Add the `Workload` type to the object model

File: `internal/api/api.go`. Add this next to the Deployment block (after the
`ReplicaSet` type is fine). It mirrors `Deployment` exactly, reusing the existing
`WorkloadStatus` type that's already defined in this file.

```go
// --- Workload (the batteries-included Deployment replacement; see docs/VOCABULARY.md)

type Workload struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta   `json:"metadata"`
	Spec     WorkloadSpec `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type WorkloadSpec struct {
	Replicas int               `json:"replicas"`
	Selector map[string]string `json:"selector"`
	Template PodSpec           `json:"template"`
}

func (w *Workload) GetMeta() *ObjectMeta { return &w.Meta }
func (w *Workload) GetType() TypeMeta    { return w.TypeMeta }
func (w *Workload) DeepCopyObject() Object {
	cp := *w
	cp.Meta = copyMeta(w.Meta)
	cp.Spec.Selector = copyStringMap(w.Spec.Selector)
	cp.Spec.Template = copyPodSpec(w.Spec.Template)
	return &cp
}
```

Note: `replicas>=1 always` is a validation rule, not a type change — you'll
enforce a sane default in the reconciler (Step 3), not here.

**Check:** `go build ./internal/api/` succeeds.

## Step 2 — Register the kind

File: `internal/api/registry.go`. Add one case to the `switch` in `New`:

```go
	case "Workload":
		return &Workload{}, nil
```

**Check:** `go build ./...` succeeds. Without this, applying a Workload returns
`unknown kind "Workload"`.

## Step 3 — Write the Workload→ReplicaSet controller

Create a new package `internal/controller/workload/workload.go`. This is
`internal/controller/deployment/deployment.go` with `Deployment`→`Workload` and
one added rule: default `replicas` to 1 when unset (the "always scales" invariant).

```go
// Package workload reconciles Workloads into ReplicaSets: each Workload owns one
// ReplicaSet named "<workload>-rs" carrying the same replica count (min 1),
// selector, and pod template. Level-triggered. See docs/VOCABULARY.md.
package workload

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/store"
)

type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string    { return "workload" }
func (r *Reconciler) Kinds() []string { return []string{"Workload"} }

func (r *Reconciler) MapToReconcileKey(ev store.Event) (store.Key, bool) {
	m := ev.Object.GetMeta()
	return store.Key{Kind: "Workload", Namespace: m.Namespace, Name: m.Name}, true
}

func rsName(workload string) string { return workload + "-rs" }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	if key.Kind != "Workload" {
		return nil
	}
	obj, ok, err := r.Client.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return nil // deleted; cascade handled elsewhere
	}
	w := obj.(*api.Workload)

	// Invariant: a Workload always scales. Treat unset/<1 as 1.
	replicas := w.Spec.Replicas
	if replicas < 1 {
		replicas = 1
	}

	want := &api.ReplicaSet{
		TypeMeta: api.TypeMeta{Kind: "ReplicaSet", APIVersion: api.APIVersion},
		Meta: api.ObjectMeta{
			Name:      rsName(w.Meta.Name),
			Namespace: w.Meta.Namespace,
			Labels:    w.Spec.Selector,
			OwnerRefs: []api.OwnerRef{{Kind: "Workload", Name: w.Meta.Name, UID: w.Meta.UID}},
		},
		Spec: api.ReplicaSetSpec{
			Replicas: replicas,
			Selector: w.Spec.Selector,
			Template: w.Spec.Template,
		},
	}

	cur, ok, err := r.Client.Get(ctx, store.Key{Kind: "ReplicaSet", Namespace: w.Meta.Namespace, Name: rsName(w.Meta.Name)})
	if err != nil {
		return err
	}
	if !ok {
		_, err := r.Client.Apply(ctx, want) // create
		return err
	}
	rs := cur.(*api.ReplicaSet)
	if rs.Spec.Replicas != want.Spec.Replicas || !sameTemplate(rs.Spec.Template, want.Spec.Template) {
		rs.Spec = want.Spec
		_, err := r.Client.Apply(ctx, rs)
		return err
	}
	return nil
}

func sameTemplate(a, b api.PodSpec) bool {
	if len(a.Containers) != len(b.Containers) {
		return false
	}
	for i := range a.Containers {
		if a.Containers[i].Image != b.Containers[i].Image || a.Containers[i].Name != b.Containers[i].Name {
			return false
		}
	}
	return true
}
```

Why `<name>-rs` (same as Deployment's RS name): a Workload and a Deployment with
the same name would collide on the RS. That's fine for this cut — you won't name
both the same thing — but note it. (If you want to be safe, use `<name>-wl-rs`.)

**Check:** `go build ./internal/controller/workload/` succeeds.

## Step 4 — Register the controller in the node agent

File: `cmd/neko/main.go`. Two edits.

1. Add the import (next to the other controller imports near the top):
```go
	"github.com/neko-box/neko/internal/controller/workload"
```

2. In `runNodeAgent`, find the `controller.Run(ctx, acc, ...)` call (it lists
`&deployment.Reconciler{...}` and `&replicaset.Reconciler{...}`). Add the Workload
reconciler to that list:
```go
		err := controller.Run(ctx, acc,
			&deployment.Reconciler{Client: acc},
			&replicaset.Reconciler{Client: acc},
			&workload.Reconciler{Client: acc},
		)
```

**Check:** `go build ./...` and `go vet ./...` succeed.

## Step 5 — Add an example

Create `examples/workload.toml` (copy `examples/deployment.toml`, change `kind`):

```toml
# A neko-native Workload — the batteries-included Deployment replacement.
# The workload controller creates a ReplicaSet; the replicaset controller creates
# Pods; the binder places them; the litterbox runs them.
kind = "Workload"
apiVersion = "neko/v1"

[metadata]
name = "web"
namespace = "default"

[spec]
replicas = 2

[spec.selector]
app = "web"

[[spec.template.containers]]
name = "web"
image = "nginx:alpine"
ports = [80]
```

## Step 6 — Unit test the reconciler

Create `internal/controller/workload/workload_test.go`. Use an in-memory store
through `access.InProc` (see `internal/access` and how existing controller tests
set up — `internal/controller/controller_test.go` is the model). Minimum two tests:

1. **Creates the RS:** apply a Workload (replicas=2) → run `Reconcile` on its key
   → assert a `ReplicaSet` named `<name>-rs` exists with `Replicas==2`, the same
   selector, and an OwnerRef of kind `Workload`.
2. **Defaults replicas to 1:** apply a Workload with `replicas` unset (0) →
   `Reconcile` → assert the RS has `Replicas==1`.

Mirror the assertion style in `internal/controller/controller_test.go`. Keep it to
the reconciler's own logic (don't spin up the full `controller.Run`).

**Check:** `go test ./internal/controller/workload/` passes.

## Step 7 — Full build + test

```sh
go build ./...
GOOS=linux GOARCH=arm64 go build ./...   # the node target must build too
go vet ./...
go test ./...
```
All must be green before the live check.

## Step 8 — Live end-to-end verification

This is the real proof. You need Docker running and the node image built.

```sh
make node-image          # rebuild so the baked neko binary includes your changes
make build               # host-side neko binary
./neko up --name wl
./neko apply examples/workload.toml
# wait for both pods to reach Running (poll a few times):
./neko get pod
```

**Success:** `get pod` shows two pods (`web-rs-0`, `web-rs-1`) both
`phase=Running` with distinct IPs — the Workload lowered to a ReplicaSet, which
created two pods, which the litterbox ran. (This is the same end state as the
Deployment path; you've just reached it from a `Workload`.)

Also confirm the chain explicitly:
```sh
./neko get ReplicaSet    # a "web-rs" exists, owned by the Workload
```

Tear down:
```sh
./neko down --name wl
```

## Step 9 — Commit

```sh
git add internal/api/api.go internal/api/registry.go \
        internal/controller/workload/ cmd/neko/main.go examples/workload.toml
git commit   # message: "workload: add Workload kind lowering to ReplicaSet"
```

---

## Done / not done

Done: `Workload` is a real, declarable kind that runs pods via the existing path,
alongside the current Deployment/ReplicaSet.

Deliberately NOT done (tracked in `docs/VOCABULARY.md`):
- Auto-observability (blocked on the pod-RTT netns gap, `docs/PROGRESS.md`).
- Config/Secret live reload (needs new `ConfigMap`/`Secret` kinds + a reload
  controller + a reload-semantics decision).
- Retiring `Deployment` in favor of `Workload`.
- `neko import` mapping k8s Deployments → `Workload`.

## If something goes wrong

- `unknown kind "Workload"` on apply → Step 2 (registry) was missed.
- Workload applies but no ReplicaSet/pods appear → Step 4 (controller not
  registered in `main.go`), or the node agent is running an OLD baked binary
  (rebuild: `make node-image` then `./neko down`/`up`).
- Pods created but stuck `Pending` → that's the binder/litterbox path, not your
  code; check the node agent log inside the container.
- Build fails on `DeepCopyObject` → you aliased a map/slice; copy it via the
  `copy*` helpers in `internal/api/deepcopy.go`.
