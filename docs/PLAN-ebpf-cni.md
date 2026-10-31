# eBPF + CNI — implementation plan (for coding by hand)

Task-style checklist for the neko-box dataplane. Two independent programs under
`bpf/`, their Go loaders in `internal/nekonet` + `internal/dataplane`, plus the
CNI story in `internal/cni`. Ordered so each task is verifiable before the next.

Grounding (verified on this machine, see `docs/PLATFORM.md`):
- Kernel 6.10-linuxkit, BTF present, cgroup v2, bpffs mountable, `cgroup_sock_addr`
  + `sock_ops` program types available. The node runs on `kindest/node:v1.33.1`
  (containerd v2.1.1), provisioned by kind (`neko up`).
- neko-net's on-wire ring buffer record is `RttEvent{daddr u32, dport u16, _pad
  u16, srtt_us u32}` (`~/neko-net/neko-net-common/src/lib.rs`) — 12 bytes, little
  endian. That is the exact layout the Go side must decode today.

Decisions this implements: **D2** (neko-box owns + loads both BPF objects; neko-net
vendored as a compiled `.o`, its daemon does NOT run), **D6** (Service LB is a
`BPF_CGROUP_INET4_CONNECT` connect-hook, not sock_ops), **D10** (youki later;
irrelevant to this plan).

---

## Phase 0 — build toolchain for eBPF (prereq)

- [ ] **0.1 Pick the BPF language.** C/libbpf (clang + `bpf2go` from cilium/ebpf)
      OR Rust/aya. Recommendation: **C + cilium/ebpf's `bpf2go`** for the Go side,
      because the loaders are Go and `bpf2go` generates Go bindings + embeds the
      `.o`. (aya is nicer if you want all-Rust, but then the loader is Rust too —
      against D1's "Go userspace".) neko-net's RTT prog is aya/Rust today; for
      neko-box, re-implement the same `sock_ops` logic in C so both progs share
      one toolchain. Keep neko-net as the reference, not a build dep.
- [ ] **0.2 Add `cilium/ebpf` to go.mod.** `go get github.com/cilium/ebpf`.
- [ ] **0.3 Decide where compilation happens.** eBPF compiles on Linux. Options:
      (a) compile inside the node container at `neko up` time; (b) compile in a
      builder image and bake the `.o`s into a custom node image; (c) `go:embed`
      pre-built `.o`s committed to the repo. Recommendation: **(c) `go:embed`** —
      commit `svc_lb.o` and `rtt.o`, loaders embed them. Document the
      `make bpf` target that regenerates them (clang in a Linux container).
- [ ] **0.4 Verify:** `clang -target bpf -O2 -g -c bpf/svc_lb/svc_lb.c` produces
      an object; `bpftool prog load`/`cilium-ebpf` can load it in the node.

---

## Phase 1 — RTT observability (lower risk; proves the load path)

Reuse neko-net's logic; neko-box owns the lifecycle.

- [ ] **1.1 Author `bpf/rtt/rtt.c`** — a `sock_ops` program mirroring
      neko-net's: on `BPF_SOCK_OPS_{ACTIVE,PASSIVE}_ESTABLISHED_CB` set
      `BPF_SOCK_OPS_RTT_CB_FLAG`; on `BPF_SOCK_OPS_RTT_CB` emit one `RttEvent`
      (same 12-byte `{daddr,dport,_pad,srtt_us}` layout) to a `BPF_MAP_TYPE_RINGBUF`.
- [ ] **1.2 `bpf2go` generate** Go bindings for it (or hand-write the `cilium/ebpf`
      `CollectionSpec` load). Output embedded in `internal/nekonet`.
- [ ] **1.3 Implement `internal/nekonet.Dataplane.Load`** — load the collection,
      attach the `sock_ops` prog to the node cgroup (`/sys/fs/cgroup`), open the
      ring buffer reader. (This is the SOLE loader — neko-net's daemon never runs.)
- [ ] **1.4 Implement `ReadRTT`** — drain the ringbuf; decode 12 bytes →
      `nekonet.Sample{DstIP, DstPort, RTTMicros}` (src/timestamp stay zero until
      neko-net M1.5 enriches the kernel-side record — tracked as risk M5b). Decode
      little-endian, `daddr` is network byte order → `netip.AddrFrom4`.
- [ ] **1.5 Wire into `internal/observ.Rollup`** — `node-agent` starts a goroutine:
      `ReadRTT` → `Rollup.Ingest`; join `DstIP` → pod/service via the store
      (list EndpointSlices/Pods, match IP). Serve `GET /v1/latency/service/{ns}/{name}`.
- [ ] **1.6 Verify end to end:** `neko up`; apply a pod; curl it; `neko latency
      svc/...` (or the raw endpoint) shows non-zero srtt. Confirms the whole
      load→attach→drain→enrich path on a real node.

---

## Phase 2 — Service LB connect-hook (the D6 kube-proxy replacement)

This is the harder, novel program. A ClusterIP has no interface; we DNAT at
`connect()` time.

- [ ] **2.1 Define the shared map layout** (one header, used by both the C prog
      and the Go writer). `svc_lb` map: `BPF_MAP_TYPE_HASH`,
      key = `{__be32 clusterip; __be16 port; __u8 proto; __u8 _pad}`,
      value = `{ backend_count; backends[MAX_BACKENDS] of {__be32 ip; __be16 port} }`.
      Pick `MAX_BACKENDS` (e.g. 64). Also a `svc_rev` map for GETSOCKNAME fixups
      (key = socket cookie → original ClusterIP:port) so apps still see the VIP.
- [ ] **2.2 Author `bpf/svc_lb/svc_lb.c`** (replace the `.c.todo`):
      - `BPF_CGROUP_INET4_CONNECT`: look up `svc_lb[dst]`; if hit, pick a backend
        (start with `bpf_get_prandom_u32 % count`, or round-robin via a per-CPU
        counter), rewrite `ctx->user_ip4`/`ctx->user_port`, store the original in
        `svc_rev` keyed by `bpf_get_socket_cookie`.
      - `BPF_CGROUP_INET4_GETSOCKNAME` (+ `GETPEERNAME`): restore the VIP from
        `svc_rev` so `getsockname()` returns the ClusterIP.
      - (Defer UDP `SENDMSG`/`RECVMSG` to a later pass; TCP first.)
- [ ] **2.3 Implement `internal/dataplane`** with `cilium/ebpf`:
      - `New()` — load the collection, attach the connect4 + getsockname4 progs to
        the node cgroup, pin/open the `svc_lb` map.
      - `SetService(clusterIP, port, proto, backends)` — write/replace the map entry
        (diff-based, idempotent).
      - `RemoveService(...)` — delete the entry.
- [ ] **2.4 Verify in isolation:** manually `SetService(10.96.0.10:80 →
      [podIP:80])`, then from inside the node `curl 10.96.0.10` hits the pod.
      This proves the DNAT before any controller is involved.

---

## Phase 3 — the Service controller (ties LB to the API)

- [ ] **3.1 Implement `internal/controller/service`** (skeleton exists):
      watches `Service`, `Pod`, `EndpointSlice`. On change: find Pods matching the
      Service selector that are Ready with a PodIP → build/update the
      `EndpointSlice` object (two sinks, one reconcile) AND call
      `dataplane.SetService` with the backends. `MapToReconcileKey`: Service→self,
      Pod→each Service whose selector matches (or re-list Services), EndpointSlice→
      owning Service.
- [ ] **3.2 Assign ClusterIPs.** Add a tiny allocator (Service CIDR
      `10.96.0.0/12`) — either in the service controller or a small `svcipam`.
      Write `Spec.ClusterIP` on first reconcile if empty (CAS).
- [ ] **3.3 Wire the service controller + `dataplane` into `node-agent`** (next to
      deployment/replicaset controllers), gated on the dataplane loading (needs the
      node, like the litterbox).
- [ ] **3.4 Verify end to end:** apply a Deployment + a Service; from inside the
      node, `curl <clusterIP>` load-balances across the backend pods.

---

## Phase 4 — CNI (make neko own pod networking, replacing the ptp stopgap)

Today `neko up` installs a stock `ptp`+`host-local` conflist (see
`internal/cni.Conflist`) and containerd's CRI plugin runs it. That's fine for M1.
Owning the CNI is only needed when we want the eBPF dataplane to see pod traffic
on our terms / per-pod attach.

- [ ] **4.1 Decide scope.** For M2, KEEP the ptp+host-local conflist — it already
      gives pods IPs on `10.244.0.0/16` and the connect-hook (attached at the node
      cgroup) already sees all pod connections. A custom CNI binary is NOT required
      for Service LB to work. **Only** write a neko CNI plugin if you need per-pod
      cgroup attach or custom veth/eBPF-at-the-veth (tc/XDP) later.
- [ ] **4.2 (If pursued) neko CNI plugin:** a small binary implementing the CNI
      ADD/DEL spec (stdin config, netns, veth, IPAM). Install it to `/opt/cni/bin`
      and emit a conflist naming it. `internal/cni` grows `Conflist` variants.
      This is a bigger effort; defer unless a concrete need appears.

---

## Cross-cutting

- [ ] **Preflight (M3):** `neko up` should run the platform probe (BTF, cgroup v2,
      bpffs, `cgroup_sock_addr`/`sock_ops` availability) and fail loudly before
      loading. The checks are the ones in `docs/PLATFORM.md`.
- [ ] **Teardown:** detach progs + close maps on `node-agent` shutdown; `neko down`
      already removes the whole node so leaked pins die with it.
- [ ] **Tests:** map-layout round-trip (Go writer ↔ C reader struct) is the easy
      unit test — keep the key/value structs in one shared header and assert Go's
      `binary.Size`/offsets match. The attach/DNAT paths are integration-tested in
      the node (like `hack/crismoke.go`): add `hack/lbsmoke.go` and `hack/rttsmoke.go`.

## Suggested order
Phase 0 → Phase 1 (proves load path, low risk, delivers a standout feature) →
Phase 2 → Phase 3 (delivers the other standout: real Service LB) → Phase 4 only
if needed. Each phase ends with a node-level smoke that proves it before moving on.
