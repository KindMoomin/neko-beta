# neko-box — Interrogation findings

Adversarial review of `ARCHITECTURE.md` + `DECISIONS.md` by two independent
reviewers (pstack `interrogate` intent, `opus high` ×2): one attacking technical
feasibility on the real platform, one attacking product coherence vs the real
alternatives. Findings that survived as load-bearing, most-severe first.

## Blockers

### B1 — `sock_ops` cannot load-balance. Wrong eBPF hook. (feasibility)
The design routes ClusterIP Service LB through a `svc_lb` map read by a
`sock_ops` program. `sock_ops` is a *notification* hook — by the time it fires,
the 4-tuple is chosen and the SYN is out; it cannot rewrite the destination. A
ClusterIP has no interface/listener, so `connect()` fails before any callback.
**Correct hook: `BPF_CGROUP_INET4_CONNECT`** (+ `INET6_CONNECT`, UDP
`SENDMSG`/`RECVMSG`, and `GETSOCKNAME` fix-up) — connect-time DNAT, exactly how
Cilium's kube-proxy replacement works. That LB program is a *second, separate*
program from neko-net's RTT `sock_ops`, and **it must be compiled in C/Rust —
Go cannot author it**. So D2 ("eBPF isn't Go") and D6 ("LB is a map lookup")
collide: the map is useless without a connect-hook program reading it, and
nobody currently owns writing that program.
→ **Fix:** rewrite D6/dataplane around a connect-hook LB program (own BPF object,
own loader); stop describing it as "a map Go writes."

### B2 — "open neko-net's pinned ringbuf" is impossible as written. (feasibility)
Confirmed against neko-net source: `neko-net/src/main.rs` does
`take_map("RTT_EVENTS")` + its own `AsyncFd` — **nothing is pinned**, no bpffs
path exists. And a `BPF_MAP_TYPE_RINGBUF` is **single-consumer**: neko-net's own
daemon already drains it, so neko-box can't also drain it without splitting
samples. Loader ownership is unresolved (two loaders would double-attach).
→ **Fix (a real decision):** neko-box becomes the **sole loader/consumer** —
neko-net ships as a *vendored compiled `.o`*, not a running process; neko-box
loads+attaches it via cilium/ebpf and owns the ringbuf. "EXTERNAL, already
attached, we only consume" is false and must change.

### B3 — The RAM thesis is benchmarked against the wrong competitor. (coherence)
The doc compares only to kind (~600 MB–1 GB) for a 5–8× win. The real
competitor is **k3s**: a *conformant, fully kubectl/Helm-compatible* server that
already uses SQLite-not-etcd and folds apiserver+controllers+scheduler+kubelet
into one process — the same monolith lever neko-box claims. k3s idles
~250–400 MB. So the honest number is **~2–3× over k3s, not 5–8×** — and by the
doc's *own* savings breakdown, the biggest RAM wins (no etcd, one Go runtime)
come from the **in-process-store + monolith** decisions, which k3s-style
compat-preserving designs also capture. The saving attributable *specifically*
to the own-protocol bet (no OpenAPI/admission/conversion) is the **smallest**
item and single-digit MB.
→ **Consequence:** the thesis "we drop compatibility to save RAM" is undercut by
its own accounting. Re-baseline against k3s/k0s and state the real multiple.

### B4 — "Own protocol, not kubectl-compatible" (D3) may trade the whole ecosystem for single-digit MB. (coherence)
Follows from B3. Steelman survives: for a disposable local loop, familiar
*shapes* + muscle memory deliver most of the felt value, and the deleted
machinery (admission/conversion/aggregation/managed-fields) is genuinely heavy.
But: (1) "kubectl/Helm/operators won't work" deletes the main reason people want
a local Kubernetes — to run artifacts they already have; (2) the RAM saving is
conflated with D4 (orthogonal to the wire protocol); (3) there's a **cheap
middle the doc never considers** — a thin kube-shaped REST+watch surface
(core verbs + resourceVersion + a minimal discovery/`openapi/v2`) that
client-go/kubectl can talk to, *without* admission/aggregation/conversion
(we're already single-version). That keeps the ecosystem for far less than a
full apiserver and nowhere near its RAM.
→ **Fix:** this is a genuine fork in the product. Decide D3 on evidence, not
assumption (see "Decision needed" below).

## Majors

### M1 — "Instant snapshot/restore" restores object records, not a running cluster.
Store+WAL captures declared state (objects, revisions, maybe eBPF map state). It
does **not** capture running container processes/memory, containerd's state DB,
open TCP connections, netns/veth runtime, or volumes. Restoring re-declares the
pods and the kubelet **re-runs** them from spec — a re-run, not a restore. The
"plus a container checkpoint" clause hides the actual hard problem (CRIU:
fragile, fails on TCP/fds/many runtimes). The "reproducible bug repro" pitch
implies live-state capture, which this doesn't do.
→ **Fix:** scope it honestly as **"snapshot/restore of declared cluster state"**
(still genuinely useful: reset-to-known-set, demos) and drop the live-process
implication — or pull CRIU in as a first-class, separately-risk-rated workstream
with a known-unsupported list.

### M2 — The `StoreAccess` seam leaks on the `Watcher` half when it goes remote.
Reader/Writer (Get/List/Apply/Delete) translate cleanly to HTTP — those are
fine. But in-proc `Watch` (an ordered, never-partial Go channel) and HTTP/SSE
watch (disconnects, idle timeouts, reconnect gaps, mid-stream `ErrTooOld`,
backpressure as a stalled socket) have different semantics. Level-triggering
saves us from *missed events* — but only if something *triggers a relist*; the
reconnect/resync/backoff/ctx-teardown logic lives at-or-above the seam and does
**not** exist in the in-proc impl. So "nothing above the seam changes" is false
for the watch path. Also: the interface currently has **one** implementation
justified by a *promised* second — the exact pattern the "no interface without
≥2 impls" rule guards against.
→ **Fix:** (1) write the `Watcher` *contract* explicitly (ordering, who owns
resync, what `ErrTooOld` means mid-stream); (2) build a loopback-HTTP `Watcher`
in M1/M2 and run controllers against it with a forced mid-stream disconnect — if
they survive unchanged, the seam is validated *now*, cheaply; (3) soften the
prose to "Reader/Writer unchanged; the Watcher adapter owns reconnect/resync."

### M3 — eBPF viability in the Docker-VM kernel is asserted, not verified.
It's not "macOS" — it's "whatever Linux VM the user installed" (Docker Desktop /
colima / Rancher, each a *different* kernel). Hard deps go unnamed: kernel floor,
`CONFIG_BPF_LSM`/`CONFIG_CGROUP_BPF`, **BTF presence** (absent → CO-RE
relocations fail), cgroup v2 layout, bpffs mount, `CAP_BPF` (≥5.8). And the
per-pod-vs-root cgroup **attach point** differs from neko-net's root-cgroup demo
— inside a container, root cgroup scopes the *container*, not individual pods.
→ **Fix:** add a "Platform floor" section (min kernel, required CONFIG/BTF, exact
mounts/caps) + a `neko up` **preflight** that probes BTF + attach-type support
and fails loudly; resolve the attach-point question (it decides whether LB and
observability scope correctly).

### M4 — Nested containerd/CNI is realistic but underspecified; it's M1's make-or-break and gets two lines.
kind proves the shape, but via a carefully built node image with aligned
cgroup driver and mounts. Open risks: cgroup-driver mismatch (cgroupfs vs
systemd) = cryptic start failures; pod CIDR colliding with Docker's `172.17/16`
or the colima network; and a **protocol confusion** — the doc says
"containerd/ttrpc" but CRI is normally **gRPC** (`k8s.io/cri-api`); ttrpc is
containerd's internal services. Pick CRI-gRPC (lower risk, matches the
ecosystem claim) and be consistent.
→ **Fix:** specify the node base image (reuse `kindest/node` or document our
own), pin the cgroup driver both sides, reserve a non-colliding pod CIDR, and
commit to CRI-gRPC.

### M5 — M1 ("bare Pods only") is a spine demo, not a usable product.
No Deployments (nobody ships bare Pods), no Services/DNS (pods can't find each
other), and **both** standout features deferred to M2 — so M1 is "a heavier
`docker run`." Fine as an *internal* milestone (it proves the
store→kubelet→CRI→CNI spine), but it must not be framed as the smallest
*useful* release. Worse, the flat "deferred" bucket hides the genuinely hard,
schedule-risk items: **DNS** (Services are half-useless without it; CoreDNS vs
eBPF-stub is unsolved), **live snapshot/CRIU** (M1 above), the **HTTP watch
adapter** (M2 above), and a **hard external dependency on neko-net's unshipped
M1.5** for the richer RTT `Sample` layout (neko-box doesn't control that repo).
→ **Fix:** relabel M1 "internal spine milestone"; define the first *user-facing*
milestone as Deployment + Service + DNS + one standout feature; move
DNS/CRIU/watch-adapter/neko-net-dependency into a named **risk register**.

## Minor

### m6 — WAL durability window + unvalidated cross-process watch resume.
Store single-writer model is **sound** (single `Apply` funnel + CAS +
deep-copy reads + level-triggered reconcilers — reviewers explicitly endorsed
this). Two gaps to document: (1) fsync policy — "WAL-append then publish" without
fsync means a crash can leave the store *behind* real container state; acceptable
for a disposable cluster, but say so and lean on level-triggering to self-heal;
(2) add a watch resume/compaction/overflow conformance test against the in-proc
impl now, so the future HTTP impl has a spec to meet (ties to M2).

## What both reviewers endorsed (don't "fix" these)
- In-process store + WAL + monotonic revision + optimistic-concurrency CAS as
  the single mutating funnel, deep-copy-only reads.
- Level-triggered reconcilers as the self-healing substrate (load-bearing).
- Scheduler → `placement.Pick` function (right scope).
- Keeping `Store` concrete and resisting speculative interfaces — *except* the
  `StoreAccess` seam, which is the one interface built ahead of its 2nd impl (M2).
- eBPF Service LB as a direction (lighter than iptables) — but via the correct
  hook (B1), not `sock_ops`.

## Root causes (two, each spanning multiple findings)
1. **neko-net is an RTT *observer*, not a reusable dataplane.** It has no
   pinning, a single-consumer ringbuf, no LB capability, and a thin payload.
   B1+B2+M3 all flow from treating it as more than it is. neko-box needs its own
   BPF programs (connect-hook LB + its own loader owning the RTT map) — reshaping
   D2/D6 and killing the "cgo-free, we only consume" story.
2. **The RAM win was priced to the wrong competitor.** B3+B4 flow from
   comparing to kind instead of k3s; once re-baselined, the own-protocol bet (D3)
   must stand on non-RAM grounds or be replaced by a cheap wire-compat middle.
