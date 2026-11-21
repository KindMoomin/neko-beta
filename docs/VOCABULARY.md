# Design sketch: batteries-included object vocabulary (PROPOSED, DEFERRED)

Status: **proposed, not built.** This is a design direction captured for later —
it does **not** describe current code. Today neko-box uses k8s-shaped kinds
(Pod, Deployment, ReplicaSet, Service, EndpointSlice, Namespace). This sketch is
where we'd go instead of copying Kubernetes 1:1. Nothing here is implemented;
M2/M3 continue on the current kinds.

## Why diverge from Kubernetes at all

Kubernetes gives you orthogonal primitives and makes *you* compose them: a Pod,
wrapped by a ReplicaSet, wrapped by a Deployment, fronted by a Service, plus a
reloader sidecar for config changes and a ServiceMonitor for metrics. Six
objects to run one app the normal way.

neko-box has a **closed, compiled-in set of kinds** (no CRDs, no discovery, no
RBAC). That is exactly what lets us be opinionated: we can make the *common
composition the primitive*, and make the operational wants people always add
(observability, config reloads) **defaults instead of add-ons**. k8s can't,
because it must stay a neutral substrate for arbitrary third parties. We don't.

The k8s three-way naming scheme (Kind `Pod` / resource `pods` / short name `po`)
exists only to serve CRD discovery + RBAC + REST routing. We have none of those
pressures, so we keep **one canonical kind spelling** on the wire and store, and
only *forgive* input at the CLI edge (see "Near-term" below).

## The proposed vocabulary

### `Workload` — the primary object (replaces Pod + Deployment + ReplicaSet)

There is **no bare pod**. The thing you declare is the thing that scales. A
Workload always carries replica semantics, even at `replicas = 1` — so there's
no "promote a Pod to a Deployment later" step, and no three-layer
Pod→RS→Deployment trinity to reason about.

Batteries included, **on by default**, no extra objects:
- **Observability** — the eBPF RTT/latency stream (the D2 standout) is simply
  *on* for every Workload. Mechanism already exists: the RTT `sock_ops` program
  attaches at the node cgroup and sees all pod traffic; per-Workload latency is
  the enrichment join we already planned in `internal/observ`. "On by default"
  is near-free here, vs. k8s needing Prometheus + a ServiceMonitor CRD.
- **Secret / ConfigMap live reload** — when a mounted secret, credential, or
  configmap a Workload consumes changes, the Workload reloads automatically
  (instead of the k8s default: nothing happens until you manually bounce pods).

Open forks to resolve when we build it:
1. **Reload semantics** — rolling restart (universal, drops connections) vs.
   in-place SIGHUP (needs app cooperation). We'd be picking a default apps
   depend on; k8s punts this entirely.
2. **Reload data-model cost** — to reload-on-change we must know which
   secrets/configmaps a Workload consumes, so the spec references them
   explicitly (it would anyway) and a controller watches those refs. That's a
   real controller, not a freebie.

### `Network` — the addressable front (replaces Service)

"Network" is the noun for *how a Workload is reached* — named, DNS-resolvable,
and backed by the eBPF connect-hook LB (D6). Open fork: is load-balancing
**always** implied (Network always programs the `svc_lb` map), the way
observability is always implied for Workload? Leaning yes — that's the point of
batteries-included — but confirm when building.

### `Task` — the escape hatch below Workload (one-shot runs)

`Task` is the one-shot, below-Workload primitive: run an arbitrary container
once. It exists because if Workload is the floor, debugging/migrations/seeds
need a home. Interactive debugging (`neko exec` into a Workload) is a *separate*
future concern, not a Task.

Designed against the ways k8s **Jobs** frustrate a developer:
- Jobs are near-immutable (re-run = delete + recreate).
- Completion semantics are a five-field puzzle (`completions`, `parallelism`,
  `backoffLimit`, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`).
- Getting output is miserable (hashed pod name; logs TTL away).
- The exit code — the thing you care about — is buried in container status.
- `backoffLimit` retries silently with opaque backoff.
- Cleanup is a landmine (pile up forever, or logs vanish before you read them).

The through-line: Jobs optimize for an **unattended controller**; a developer
wants an **attended command**. Task inverts the priorities — the win is in the
*verbs*, not the spec.

Decisions locked:
- **Lighter family member.** Task auto-injects secrets/config (a migration needs
  its DB credential) but has **no** RTT/observability and **no** reload-on-change
  — those are Workload concerns; a Task is short-lived.
- **`neko run <task>` blocks by default**: streams logs live and **returns the
  task's exit code as neko's own exit code**. `--detach` to fire-and-forget.
  This asymmetry with `neko apply` (returns immediately) is intentional — `run`
  is a different verb with different intent. This single choice fixes the
  output/exit-code/retry pains at once.
- **Clear status:** `Pending → Running → Succeeded | Failed(exitCode=N) |
  TimedOut` — an enum plus the real process exit code, not a counter.
- **No scheduling.** No Cron/`schedule` field (that reintroduces the
  unattended-controller model we're escaping). Design field names so a trigger
  *could* slot in later, but it's out of scope.

Strawman spec (react to when we build it):
```toml
kind = "Task"
[metadata]
name = "migrate-db"
[spec]
image = "myapp:latest"
command = ["./migrate", "--up"]
retries = 0          # default 0 — a dev task fails loud, no silent retries
timeout = "5m"       # wall-clock; clearer than activeDeadlineSeconds
keepFor = "1h"       # retain logs+status after finish (default: until `neko task rm`)
```

**TODO (late roadmap):** make Task behave like an **invocation/Lambda model** —
ephemeral, invoke-and-forget, each run independent. The re-run/attempt-history
mechanics (reset-in-place vs. a short ring of recent attempts) are deferred;
Workloads are the near-term focus. Design so the Lambda-style invocation model
can layer on without reshaping the object.

## Migration from today's kinds

`neko import` (D3) becomes a *lossy-upward* translation, which is cleaner than
k8s's 1:1 mapping:
- k8s Deployment/ReplicaSet/bare Pod → **Workload** (collapse the trinity).
- k8s Service → **Network**.
- k8s Job/CronJob → **Task** (drop CronJob's schedule unless scheduling lands).

Internally, a Workload may still reconcile *down* to the current Pod/ReplicaSet
machinery at first (the controllers already exist) — the vocabulary is the
user-facing surface; the lowering is an implementation detail we can keep or
flatten later.

## Near-term (DONE — independent of this sketch)

The `neko get` CLI already canonicalizes kind input client-side
(`internal/clientcmd` `canonicalKind` + `kindAliases`): `pod`/`pods`/`po` → the
one canonical `Pod`, etc. This is the deliberate anti-RESTMapper choice — a tiny
static alias table at the CLI edge, server stays strict on one spelling. When
the Workload vocabulary lands, the alias table gains `workload`/`wl`,
`network`/`net`, `task`, and the canonical kinds change; the mechanism doesn't.
