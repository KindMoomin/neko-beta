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

#### Pluggable dataplane on `Network` (neko-net's home, observer → actor)

`Network` is where the eBPF dataplane is exposed as policy, because it already
owns load-balancing. The key insight: **neko-net's RTT observer and the future
latency-aware balancer are the same dataplane.** neko-net today *watches* every
connection and emits latency; its own roadmap (M2) is to *route* on those
measurements. So the plug should be designed as "this Network participates in
the smart dataplane," not "metrics on/off" — then routing lights up as a
capability later without a redesign.

Proposed surface (sketch — build `observe` first, keep `balance` forward-compat):
```toml
[spec.dataplane]
observe = "latency"        # neko-net RTT attributed to this Network's backends
balance = "round-robin"    # today; "latency-aware" is the M2 future — route to
                           # the lowest-RTT healthy backend, fed by the observe stream
```

The layering that makes this work (and keeps neko-net a living upstream, not a
one-time copy):
- **neko-net RTT program = node-cgroup observer.** One cheap attach at the node
  container's root cgroup sees all connections (as neko-net's README describes).
  Not per-workload cgroup attach — that stays a later option (see fork 1).
- **`internal/observ` = attribution.** Slice the one node-wide stream into
  per-Workload / per-Network latency by joining the remote IP against the store
  (EndpointSlices/Pods). So data is per-workload even though the program is
  node-wide — sidestepping per-cgroup attach cost while feeling granular.
- **`Network.spec.dataplane` = policy.** `observe` exposes the latency;
  `balance = "latency-aware"` is where neko-net graduates observer → actor: the
  connect-hook `svc_lb` program reads the live RTT and picks the fastest healthy
  backend instead of round-robin. **This is the networking feature that makes
  neko-net structurally necessary** rather than a readout — and a differentiated
  local-dev feature (watch the mesh route around a slow pod, live).

How neko-net stays in the picture (vs. the C re-implementation alternative):
neko-net remains a **living upstream dependency**, not a vendored copy. neko-box
builds neko-net's compiled `.o` (its `sock_ops` RTT program) and loads it via
its own Go loader (`internal/nekonet`); neko-net's userspace daemon never runs
(D2). The shared contract is `neko-net-common`'s `RttEvent` struct. When neko-net
ships its M1.5 richer payload (src addr/port + timestamp) or M2 routing logic,
neko-box inherits it by rebuilding the object — no neko-box program rewrite.
Cost accepted: a Rust (nightly + bpf-linker + aya) toolchain for the RTT program
*and* a C/clang toolchain for the neko-box-only `svc_lb` program — two BPF
toolchains, because `svc_lb` has no neko-net equivalent.

Open forks to resolve when we build it:
1. **Node-cgroup attach + store attribution** (chosen for now) vs. true
   per-workload cgroup attach — the latter is only needed for per-workload
   *policy*, not per-workload *data*. Lean node-cgroup until a policy need appears.
   **Note (2026-11-21):** the node-root attach does NOT currently surface pod-IP
   RTT — pod sockets live in per-pod netns and the root-netns kprobe never emits
   a pod-IP destination. So per-pod attribution likely forces per-netns/per-pod
   attach sooner than this fork assumed. See the open "RTT not attributed to
   pods" issue in `docs/PROGRESS.md` (try dropping `ipMasq` first).
2. **Measurement scope** — all connections node-wide (egress included, matches
   the cheap attach) vs. only Network-backend traffic. Lean node-wide for the
   data; `Network` policy decides what to expose/act on.
3. **`balance = "latency-aware"`** — documented M2/M3 goal; build only `observe`
   first, but land the `Network.spec.dataplane` surface now so it's
   forward-compatible.

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

## Security: abstracted, guardrailed by default (PROPOSED, DEFERRED)

Kubernetes exposes the full Linux security surface directly — `securityContext`
with `privileged`, `allowPrivilegeEscalation`, `capabilities.add`, `runAsUser:
0`, `hostPID/hostNetwork/hostPATH`, and a separate opt-in PodSecurity admission
layer you have to *remember* to turn on. The default is permissive; safety is a
thing you bolt on. That's backwards for a developer box.

neko-box should invert it: **sandboxed and non-escalating by default, with a
small set of named, auditable capability levels instead of raw knobs.** Because
the kind set is closed, we can make the safe thing the default thing.

Proposed shape (react to when we build it):
- **A `sandbox` level on Workload/Task**, not a raw `securityContext`. A short
  enum of intent, each mapping to a vetted set of OCI/seccomp/capability
  settings neko-box controls:
  - `strict` (default) — non-root, `no-new-privileges`, drop ALL capabilities,
    read-only rootfs where possible, default seccomp profile, no host namespaces.
    What you get unless you say otherwise.
  - `standard` — non-root, a small allowlist of common capabilities
    (e.g. `NET_BIND_SERVICE`), writable rootfs. For ordinary apps that need a
    privileged port or a scratch dir.
  - `trusted` — escape hatch for the rare case (needs explicit opt-in + a loud
    log line). Still never silently `privileged`; privileged is its own separate,
    even-louder flag so it can't be reached by accident.
- **Privilege escalation off, always, unless `trusted`+explicit.** The common
  k8s footgun (`allowPrivilegeEscalation` defaulting on) simply isn't reachable.
- **No host namespaces / host mounts by default.** `hostPID`, `hostNetwork`, and
  host path mounts require the `trusted` level — the sandbox is the default
  boundary, not an afterthought.
- **Guardrails are enforced at the CRI boundary**, in one place
  (`internal/cri` builds the `LinuxContainerSecurityContext`), so the policy is
  centralized and testable — not scattered across per-object specs. A single
  `sandbox.go` translates the level enum → the concrete OCI settings.
- **Auditable:** the node-agent logs the effective sandbox level per Workload,
  and `neko get` can surface it, so "what can this actually do" is answerable
  without decoding a securityContext.

Open forks to resolve when we build it:
1. **Where seccomp/AppArmor profiles live** — baked into the node image (D11
   image work already preps the node), or shipped per-level by neko-box.
2. **How `trusted` is gated** — a per-node allow flag at `neko up`, a field that
   must be set in two places, or a confirm prompt. It must be hard to reach by
   accident.
3. **Interaction with `neko import`** — a k8s Pod with `privileged: true` maps to
   what? Probably refuse-by-default and require the user to re-declare intent as
   `trusted`, rather than silently honoring an imported privileged container.
4. **Rootless vs. the privileged node.** The node container itself runs
   privileged (nested containerd needs it, D11). The guardrails are about the
   *workloads inside* the node, not the node boundary — document that distinction
   so nobody assumes the node is sandboxed.



The `neko get` CLI already canonicalizes kind input client-side
(`internal/clientcmd` `canonicalKind` + `kindAliases`): `pod`/`pods`/`po` → the
one canonical `Pod`, etc. This is the deliberate anti-RESTMapper choice — a tiny
static alias table at the CLI edge, server stays strict on one spelling. When
the Workload vocabulary lands, the alias table gains `workload`/`wl`,
`network`/`net`, `task`, and the canonical kinds change; the mechanism doesn't.
