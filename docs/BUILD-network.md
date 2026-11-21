# Build guide: the `Network` object (first cut)

Audience: a developer new to this repo. Follow in order; run each check.

## What you're building

A new top-level kind, **`Network`**, the batteries-included replacement for
`Service` — the named front that routes to a `Workload`'s pods. A new controller
resolves the Network's selector to ready pods, assigns a stable ClusterIP, and
writes an `EndpointSlice` listing the backends.

**Read this scope note carefully — it's the whole trick to making this
junior-buildable.** The full `Network` vision (see `docs/VOCABULARY.md`) has TWO
layers:

1. **Control plane (THIS cut):** `Network` → ClusterIP + `EndpointSlice`. Pure
   store work. Fully buildable today, mirrors the Workload/Deployment pattern.
2. **Dataplane (NOT this cut — eBPF Phase 2):** the `svc_lb`
   `BPF_CGROUP_INET4_CONNECT` program that actually DNATs a connection to a
   ClusterIP onto a backend. `internal/dataplane` and `bpf/svc_lb/svc_lb.bpf.c`
   are still skeletons (`panic("not implemented")` / `.todo`). **Do not touch
   them in this cut.** Until Phase 2 lands, a ClusterIP won't actually carry
   traffic — but the control-plane objects (the EndpointSlice, the assigned IP)
   are real and correct, and they're exactly what the eBPF programmer will read
   later.

So: build the brain (who are the backends, what's the VIP), defer the muscle
(rewriting packets). This cut ends with a queryable `EndpointSlice` and a stable
`Network.Spec.ClusterIP`, verified against the store — NOT with a working
`curl <clusterIP>`.

## Background: the pattern you're copying

A `Network` is structurally a `Service` plus an owned `EndpointSlice`. You will:
- mirror the `Service` type for the `Network` kind (`internal/api/api.go`),
- write a controller like `internal/controller/deployment/deployment.go` but one
  that produces an `EndpointSlice` and assigns a ClusterIP.

Key facts (same as the Workload guide — skim if you've read it):
- Every kind is a struct implementing `api.Object`; `DeepCopyObject` must not
  alias maps/slices (use the `copy*` helpers in `internal/api/deepcopy.go`).
- `api.New` (`internal/api/registry.go`) is the ONE kind→type switch; add
  `Network` or the store rejects it.
- Controllers implement `controller.Controller` and are registered in the
  `controller.Run(...)` call in `cmd/neko/main.go`.
- Reconcilers are level-triggered: read desired + actual each pass, converge with
  `Client.Apply`.
- The `EndpointSlice` and `Endpoint` types ALREADY exist in `internal/api/api.go`
  (lines ~189–213) — you reuse them, you don't define them.
- Service CIDR is `10.96.0.0/12` (`docs/PLATFORM.md`). You allocate ClusterIPs
  from it.

A `Network` controller watches THREE kinds (like the Service skeleton does):
`Network` (self), `Pod` (a pod becoming ready changes a Network's endpoints), and
`EndpointSlice` (so a manual edit re-converges). The `MapToReconcileKey` step
translates each event back to the owning Network's key.

---

## Step 1 — Add the `Network` type

File: `internal/api/api.go`, near the `Service` block. Mirror `Service`, adding
`Selector` already in the spec (Service has it) and keeping `ClusterIP` as a
field the controller fills in:

```go
// --- Network (the batteries-included Service replacement; see docs/VOCABULARY.md)

type Network struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta  `json:"metadata"`
	Spec     NetworkSpec `json:"spec"`
}

type NetworkSpec struct {
	// ClusterIP is assigned by the controller on first reconcile (not authored).
	ClusterIP string            `json:"clusterIP,omitempty"`
	Selector  map[string]string `json:"selector"`
	Ports     []ServicePort     `json:"ports"` // reuse the existing ServicePort type
}

func (n *Network) GetMeta() *ObjectMeta { return &n.Meta }
func (n *Network) GetType() TypeMeta    { return n.TypeMeta }
func (n *Network) DeepCopyObject() Object {
	cp := *n
	cp.Meta = copyMeta(n.Meta)
	cp.Spec.Selector = copyStringMap(n.Spec.Selector)
	if n.Spec.Ports != nil {
		ports := make([]ServicePort, len(n.Spec.Ports))
		copy(ports, n.Spec.Ports)
		cp.Spec.Ports = ports
	}
	return &cp
}
```

**Check:** `go build ./internal/api/` succeeds.

## Step 2 — Register the kind

File: `internal/api/registry.go`. Add to the `switch`:
```go
	case "Network":
		return &Network{}, nil
```

**Check:** `go build ./...` succeeds.

## Step 3 — A tiny ClusterIP allocator

The controller needs to hand out unique IPs from `10.96.0.0/12`. Keep it dead
simple for a single-node dev box: a counter over the CIDR, skipping `.0`/`.1`.
Put it in the controller package (Step 4) as an unexported helper, OR a small
`internal/svcipam` package if you prefer. Minimal approach — a monotonic offset
into the CIDR, persisted implicitly by scanning existing Networks on startup so
you don't reassign.

The simplest correct version for this cut: on reconcile, if the Network has no
ClusterIP, pick `10.96.0.<N>` where N is `10 + (count of Networks that already
have a ClusterIP)`. Collisions are impossible on a single node with a handful of
Networks. Document that this is a stopgap; a real bitmap allocator is a later
refinement (note it in the package doc).

> Why not fancier: ClusterIP allocation is a solved, boring problem and the dev
> box never has enough Services to need a real allocator. Three lines beat a
> premature bitmap. (Project rule: no abstraction without measurable need.)

**Check:** none yet — folded into Step 4.

## Step 4 — Write the Network controller

Create `internal/controller/network/network.go`. Structure mirrors the Service
skeleton's `Kinds()` (watch Network/Pod/EndpointSlice) and the Deployment
reconciler's create-or-update shape, but it produces an `EndpointSlice` and
assigns a ClusterIP. **Do NOT import or call `internal/dataplane`** — that's the
Phase-2 muscle; this cut is control-plane only.

```go
// Package network reconciles Networks into a ClusterIP + an EndpointSlice:
// it resolves the Network's selector to ready Pods, assigns a stable ClusterIP
// (from the 10.96.0.0/12 service CIDR) on first reconcile, and writes an
// EndpointSlice named "<network>" listing the ready backend IPs. Level-triggered.
//
// NOT in this cut: programming the svc_lb eBPF map (internal/dataplane) so the
// ClusterIP actually DNATs — that is eBPF Phase 2. This controller produces the
// objects that programmer will later read. See docs/VOCABULARY.md, docs/PROGRESS.md.
package network

import (
	"context"
	"fmt"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
	"github.com/neko-box/neko/internal/store"
)

type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string    { return "network" }
func (r *Reconciler) Kinds() []string { return []string{"Network", "Pod", "EndpointSlice"} }

// MapToReconcileKey: a Network event reconciles itself. A Pod or EndpointSlice
// event reconciles every Network in that namespace (simplest correct mapping on
// a dev box — re-list Networks and let the level-triggered pass sort it out).
// For the FIRST cut you may return ok=false for Pod/EndpointSlice and instead
// rely on the Network's own events; but then endpoints only refresh when the
// Network changes. Prefer mapping Pod->its namespace's Networks so a pod going
// ready updates the slice. Implement the namespace re-list here.
func (r *Reconciler) MapToReconcileKey(ev store.Event) (store.Key, bool) {
	m := ev.Object.GetMeta()
	if ev.Object.GetType().Kind == "Network" {
		return store.Key{Kind: "Network", Namespace: m.Namespace, Name: m.Name}, true
	}
	// Pod / EndpointSlice: there's no single owning Network key to return. The
	// clean way is to enqueue all Networks in the namespace. The controller
	// interface returns ONE key, so for the first cut, return ok=false here and
	// document that endpoints refresh on the next Network event or agent restart.
	// (A follow-up can add multi-key mapping; keep this cut simple.)
	return store.Key{}, false
}

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	if key.Kind != "Network" {
		return nil
	}
	obj, ok, err := r.Client.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return nil // deleted
	}
	net := obj.(*api.Network)

	// 1. Assign a ClusterIP on first reconcile (CAS write back to the Network).
	if net.Spec.ClusterIP == "" {
		ip, err := r.allocateClusterIP(ctx)
		if err != nil {
			return err
		}
		net.Spec.ClusterIP = ip
		if _, err := r.Client.Apply(ctx, net); err != nil {
			return err // conflict -> requeued -> retried
		}
		// Fall through to also build the EndpointSlice this pass.
	}

	// 2. Resolve backends: ready Pods in this namespace matching the selector.
	pods, _, err := r.Client.List(ctx, "Pod", net.Meta.Namespace, labels.Selector(net.Spec.Selector))
	if err != nil {
		return err
	}
	var eps []api.Endpoint
	for _, o := range pods {
		p := o.(*api.Pod)
		if p.Status.PodIP == "" || !p.Status.Ready {
			continue
		}
		port := 0
		if len(net.Spec.Ports) > 0 {
			port = net.Spec.Ports[0].Port // first cut: single port
		}
		eps = append(eps, api.Endpoint{PodUID: p.Meta.UID, IP: p.Status.PodIP, Port: port, Ready: true})
	}

	// 3. Create or update the EndpointSlice named after the Network.
	want := &api.EndpointSlice{
		TypeMeta: api.TypeMeta{Kind: "EndpointSlice", APIVersion: api.APIVersion},
		Meta: api.ObjectMeta{
			Name:      net.Meta.Name,
			Namespace: net.Meta.Namespace,
			OwnerRefs: []api.OwnerRef{{Kind: "Network", Name: net.Meta.Name, UID: net.Meta.UID}},
		},
		Endpoints: eps,
	}
	cur, ok, err := r.Client.Get(ctx, store.Key{Kind: "EndpointSlice", Namespace: net.Meta.Namespace, Name: net.Meta.Name})
	if err != nil {
		return err
	}
	if !ok {
		_, err := r.Client.Apply(ctx, want)
		return err
	}
	es := cur.(*api.EndpointSlice)
	if !sameEndpoints(es.Endpoints, want.Endpoints) {
		es.Endpoints = want.Endpoints
		_, err := r.Client.Apply(ctx, es)
		return err
	}
	return nil
}

// allocateClusterIP hands out 10.96.0.<N> from the service CIDR. Stopgap: N is
// 10 + (number of Networks that already hold a ClusterIP). Fine for a single-node
// dev box; a real bitmap allocator is a later refinement.
func (r *Reconciler) allocateClusterIP(ctx context.Context) (string, error) {
	nets, _, err := r.Client.List(ctx, "Network", "", nil)
	if err != nil {
		return "", err
	}
	used := 0
	for _, o := range nets {
		if o.(*api.Network).Spec.ClusterIP != "" {
			used++
		}
	}
	return fmt.Sprintf("10.96.0.%d", 10+used), nil
}

func sameEndpoints(a, b []api.Endpoint) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, e := range a {
		seen[e.IP] = true
	}
	for _, e := range b {
		if !seen[e.IP] {
			return false
		}
	}
	return true
}
```

Notes for the implementer:
- `labels.Selector` + `.Matches` is in `internal/labels`; `Client.List` already
  filters by selector, so you may not even need `.Matches` directly.
- The `MapToReconcileKey` returning `ok=false` for Pod/EndpointSlice is a
  deliberate simplification — endpoints refresh when the Network changes or the
  agent restarts. Call this out in a comment; a follow-up adds owner mapping.
- Single port only in this cut. Multi-port is a later refinement.

**Check:** `go build ./internal/controller/network/` succeeds.

## Step 5 — Register the controller

File: `cmd/neko/main.go`. Add the import:
```go
	"github.com/neko-box/neko/internal/controller/network"
```
And add to the `controller.Run(...)` list:
```go
			&network.Reconciler{Client: acc},
```

**Check:** `go build ./...` and `go vet ./...` succeed.

## Step 6 — Example

Create `examples/network.toml`:
```toml
# A neko-native Network — the batteries-included Service replacement. The network
# controller assigns a ClusterIP and writes an EndpointSlice of ready backend pods.
# (Traffic routing to the ClusterIP is eBPF Phase 2 — not yet wired.)
kind = "Network"
apiVersion = "neko/v1"

[metadata]
name = "web"
namespace = "default"

[spec]
[spec.selector]
app = "web"

[[spec.ports]]
port = 80
targetPort = 80
```

## Step 7 — Unit test

Create `internal/controller/network/network_test.go` (model:
`internal/controller/controller_test.go`, in-memory store via `access.InProc`).
Minimum three tests:
1. **Assigns a ClusterIP:** apply a Network with empty ClusterIP → `Reconcile` →
   re-Get the Network → `Spec.ClusterIP` is non-empty and in `10.96.0.0/12`.
2. **Builds an EndpointSlice from ready pods:** pre-create two Pods (matching
   labels, `Status.Ready=true`, with PodIPs) → apply the Network → `Reconcile` →
   an `EndpointSlice` named `web` exists with both pod IPs.
3. **Excludes not-ready pods:** a pod with `Ready=false` or empty PodIP is NOT in
   the slice.

**Check:** `go test ./internal/controller/network/` passes.

## Step 8 — Full build + test

```sh
go build ./...
GOOS=linux GOARCH=arm64 go build ./...
go vet ./...
go test ./...
```

## Step 9 — Live verification (control-plane only)

```sh
make node-image && make build
./neko up --name net
./neko apply examples/workload.toml   # gives the Network some backends
# wait for pods Running:
./neko get pod
./neko apply examples/network.toml
```

**Success criteria (control-plane — NOT traffic):**
```sh
# The Network got a ClusterIP:
./neko get Network            # web shows a 10.96.0.x ClusterIP
# An EndpointSlice lists the ready pod IPs:
curl -s 127.0.0.1:7443/v1/EndpointSlice/default/web
#   -> endpoints array with the two pod IPs (10.244.0.x), ready:true
```

Do NOT expect `curl <clusterIP>` to work — the DNAT is eBPF Phase 2. If someone
asks "why can't I hit the ClusterIP," that's the muscle that isn't built yet;
point them at `docs/VOCABULARY.md` (Network dataplane) and `bpf/svc_lb/`.

Tear down: `./neko down --name net`.

## Step 10 — Commit

```sh
git add internal/api/api.go internal/api/registry.go \
        internal/controller/network/ cmd/neko/main.go examples/network.toml
git commit   # "network: add Network kind -> ClusterIP + EndpointSlice (control plane)"
```

---

## Done / not done

Done: `Network` is a declarable kind that assigns a ClusterIP and maintains an
`EndpointSlice` of ready backends — the control-plane half of the Service
replacement.

Deliberately NOT done (the muscle + the sugar, tracked in `docs/VOCABULARY.md`):
- **The eBPF `svc_lb` connect-hook DNAT** (`internal/dataplane` +
  `bpf/svc_lb/svc_lb.bpf.c`) — eBPF Phase 2. Until it lands, a ClusterIP is an
  allocated number + an endpoint list, not a reachable address. This is the big
  follow-on; it reuses the C/clang + vmlinux.h toolchain established by the RTT
  program (`bpf/rtt/`).
- `balance = "latency-aware"` (routing on the RTT stream) — depends on both the
  svc_lb program AND the pod-RTT attribution gap being fixed.
- DNS for `*.svc` (CoreDNS embed, D8).
- Multi-port, real bitmap ClusterIP allocator, Pod→Network event mapping.
- Retiring `Service` in favor of `Network`.

## If something goes wrong

- `unknown kind "Network"` → Step 2 (registry) missed.
- Network applies but no ClusterIP / no EndpointSlice → Step 5 (controller not
  registered), or the agent is running an OLD baked binary (`make node-image`
  then `./neko down`/`up`).
- EndpointSlice is empty though pods are Running → the pods aren't `Status.Ready`
  yet, or their labels don't match the Network selector. Check
  `./neko get pod` and the selector in `network.toml`.
- "Why doesn't curl to the ClusterIP work" → expected; DNAT is Phase 2 (above).
