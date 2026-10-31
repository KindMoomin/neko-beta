# neko-box — Progress

_Last updated: 2026-10-30_

## Phase: Design

Using the pstack **architect** method (Phase B "design it twice, then
synthesize") at `opus high`, per `~/.claude/rules/pstack-models.md`.

### Done
- [x] Scoped the project: local, non-production, Kubernetes-shaped, small-RAM.
- [x] Surveyed what's cuttable from stock Kubernetes.
- [x] Locked the core decisions (see `DECISIONS.md`): Go userspace + reused
      Rust/eBPF dataplane; own `nekoctl`-only protocol; in-process store + WAL
      (no etcd); cut scheduler; fold kube-proxy into eBPF Service LB.
- [x] Decided language question: **Go userspace + keep neko-net's Rust eBPF**
      (Option B), not a full Rust or a pure-Go-with-C-eBPF rewrite.
- [x] Grounded the `kind` node-as-container pattern against primary docs.
- [x] Confirmed toolchain on this machine: Go 1.26, Docker 28.3, Rust via
      rustup. (eBPF itself only builds/runs inside the Linux node container.)
- [x] Wrote `README.md` and `DECISIONS.md`.

### Done (design)
- [x] Ran two competing whole-system design variants in parallel (opus high):
      **A — monolith** (one process, store-as-truth, in-proc controllers/kubelet)
      and **B — separated processes** (apiserver/kubelet/controllers dogfood the
      public HTTP+watch API).
- [x] Synthesized into `docs/ARCHITECTURE.md`. **Chosen shape: the monolith (A)
      for its 2–3× RAM win, with B's contract drawn as a narrow
      `access.StoreAccess` seam** so multi-node is a wiring change (swap the
      in-proc adapter for an HTTP one), not a rewrite. Module map + key Go
      interface sketches + pod lifecycle + RAM budget + M1 scope all captured.

### Done (interrogation + revision)
- [x] Ran adversarial review (pstack `interrogate` intent, opus high ×2):
      feasibility + coherence. Findings in `docs/INTERROGATION.md`.
- [x] Resolved the findings with the user and revised all docs:
      - **D1/D2 (B1/B2):** neko-box owns its BPF — a `BPF_CGROUP_INET4_CONNECT`
        connect-hook LB (sock_ops can't load-balance) + neko-net's RTT program
        **vendored as a compiled `.o`** that neko-box loads/attaches/drains
        (neko-net isn't pinned and a ringbuf is single-consumer). Go is no
        longer the only userspace — real C/Rust `bpf/` build re-enters.
      - **D3 (B3/B4):** keep neko-native TOML wire (nekoctl-only) **plus a
        one-way `neko import`** (helm template / k8s YAML → neko-native → apply).
        Live k8s-API proxy = documented future nice-to-have. RAM re-baselined
        against **k3s** (~2–3× win), not kind — the win is D4, not the wire.
      - **D7 (M1-major):** snapshot/restore scoped to **declared state** only;
        CRIU live checkpoint out of scope.
      - **M2 (seam):** `StoreAccess` Reader/Writer unchanged remote; **Watcher**
        adapter owns reconnect/resync. Loopback-HTTP Watcher validated in M1.
      - **M3/M4:** Platform-floor + `neko up` preflight; node image / cgroup
        driver / non-colliding CIDR / **CRI-gRPC** (not ttrpc) — in risk register.
      - **M5:** M1 relabelled an *internal spine milestone*; **M2 = first
        user-facing release** (Deployment + Service + DNS + one standout + import).
      - Added a **risk register** to `ARCHITECTURE.md`.

### Next
- [x] Probed this machine's container kernel → **platform floor verified**
      (kernel 6.10-linuxkit: BTF, cgroup v2, bpffs, `cgroup_sock_addr` +
      `sock_ops` program types, all required CONFIG). Wrote `docs/PLATFORM.md`.
- [x] Resolved risk-register items: M3 (platform floor + preflight), M4 (node
      image `kindest/node`, cgroupfs, CIDRs `10.244/16`+`10.96/12`, CRI-gRPC),
      **M5a DNS = embed CoreDNS + custom store-backed `neko` plugin (D8)**,
      m6 (WAL fsync-before-publish). Only M5b (neko-net M1.5 payload) stays open
      as an external dependency.
- [x] **Scaffolded the Go module (architect Phase D).** `go mod init`
      `github.com/neko-box/neko`; 23 packages across `cmd/neko` + `internal/*`;
      every design type/signature present with `not implemented` bodies. **Builds
      and vets clean with ZERO external dependencies** (heavy deps — cilium/ebpf,
      k8s.io/cri-api, CoreDNS — deliberately deferred to implementation; local
      minimal `labels` stands in for apimachinery). `bpf/` has non-Go stubs +
      READMEs for the two eBPF programs. `InProc` compile-asserts it satisfies
      `StoreAccess`; `Controller` has 4 real impls (justifies the interface).

### Known design refinement (tracked, not a blocker)
- The dependency rule says "only store/access/httpapi touch store internals",
  but the controllers import `internal/store` for the `Key`/`Event` **types**
  returned across the `access` seam (they never call `*store.Store`). To make
  the rule literally enforceable, move `Key`/`Event` into a neutral package
  (e.g. `internal/api` or a small `internal/kv`) so seam types don't pull in the
  store package. Deferred — a clean refactor, best done with the first real
  store implementation.

### Next
- [x] Begin M1 implementation against the skeleton (architect Phase D fill-in).
      **Done so far (all building + passing under `go test -race`):**
      - `api`: real `DeepCopyObject` for every kind + helpers; test proves the
        store can't leak aliased pointers (risk #2).
      - `store`: full in-memory store — monotonic revision, optimistic
        concurrency (create/CAS/`ErrConflict`/`ErrExists`/`ErrNotFound`),
        deep-copy in/out, in-process pub/sub `Watch` (rev-0 replay then live
        tail, slow-consumer drop), `List` filtering. Tests cover each + a
        200-goroutine concurrent stress test under `-race`.
      - `store/wal`: length-prefixed JSON WAL, `fdatasync`-before-publish,
        torn-tail-safe replay, typed object decode. Test proves durability
        across reopen (create/update/delete all replay correctly).
      - `access.InProc`: implemented + round-trip test; maps the seam's
        revision-on-object convention to the store's `expectedRevision` arg.
- [ ] Next up (M1 continuation):
      - `httpapi`: wire the `/v1` REST + SSE-watch surface over the store.
      - `clientcmd` + `cmd/neko` client verbs: `apply`/`get`/`delete` over HTTP.
      - `cri` (CRI-gRPC) + `cni`/`ipam` + minimal `kubelet`: actually run a pod.
      - `neko up`/`node-agent` bring-up + the platform preflight.

### Not committed
No git commits yet (repo has no history/branch; awaiting go-ahead to commit).

## Milestones

- **M1 — internal spine (NOT a release):** single node container; in-process
  store + WAL; `nekoctl apply pod.toml`; node agent pulls+runs via containerd
  (CRI-gRPC); minimal veth/IPAM; loopback-HTTP Watcher validated. Bare Pods
  only — proves the mechanism, not the product.
- **M2 — first user-facing release:** Deployment/ReplicaSet + Namespace
  controllers; Service + EndpointSlice + the `svc_lb` connect-hook LB; DNS for
  `*.svc`; **one** standout feature (RTT latency *or* declared-state snapshot);
  `neko import` for a real Helm chart.
- **M3 — rounded out:** the other standout feature; multi-node (`access` HTTP
  adapter); broader object support.
- **M4 — TUI** (bubbletea) — deferred, as requested.
- **Future (documented, not scheduled):** live k8s-API proxy so kubectl/helm
  point at neko-box directly (built on the `neko import` translation core).
