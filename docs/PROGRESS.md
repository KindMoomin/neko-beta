# neko-box — Progress

_Last updated: 2026-10-31_

## Known issues (open)

- **Litterbox drops a live pod event: only 1 of N deployment pods starts on the
  initial apply.** With a Deployment `replicas>=2`, the controllers correctly
  create and bind all pods (verified: both `web-rs-0`/`web-rs-1` get
  `nodeName=node0` in the store), but the litterbox starts only ONE sandbox on
  the first watch. **All pods DO start after a `node-agent` restart** (the fresh
  watch replay reconciles them), which points at a missed/dropped *live* watch
  event, not reconcile logic. Tried (did NOT fix): moving `ensureRunning` into a
  per-pod goroutine with a per-UID busy guard (`internal/litterbox`). Ruled out:
  binder conflict (both pods end up bound); litterbox error (agent log is clean —
  `ensureRunning` is never called for the 2nd pod). **Next step:** a debug
  `log.Printf` is currently left in `internal/litterbox/litterbox.go` `handle()`
  — run `neko up` + apply `examples/deployment.toml`, read which `event
  type/pod/phase` lines actually arrive, and compare against the store's watch
  publish path (`internal/store/store.go` `publish`/`Watch`). Suspect the store
  `Watch` replay+tail hand-off or slow-consumer drop. **Remove that debug line**
  once resolved.

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
      - `api.New(kind)`: single kind→type constructor; WAL decode now reuses it
        (removed the duplicated switch), and `httpapi` request-decode uses it too.
      - `httpapi`: the full `/v1` external seam over the store — list/get/create
        (POST)/replace (PUT `?rev=`)/delete + SSE watch (`?watch=1&since=`) +
        healthz. Go 1.26 `ServeMux` pattern routing; optimistic concurrency maps
        to 409. Tested end-to-end via `httptest`: CRUD, CAS, duplicate/stale
        conflicts, and watch replay+live — all under `-race`.
      - `clientcmd` + `cmd/neko`: TOML-native client (`apply`/`get`/`delete`) over
        HTTP, and a runnable `neko node-agent` serving the store over httpapi.
        TOML decodes via TOML→map→JSON→api type (one source of truth: the json
        tags). `apply` is idempotent (GET probe → POST create or CAS PUT).
      - **End-to-end verified against the real binary:** `neko node-agent` +
        `neko apply examples/pod.toml` → created → `get` renders it →
        re-`apply` → configured → `delete` → gone; and WAL **durability across
        agent restart** (state replays from disk). Added `BurntSushi/toml` dep.
- [ ] Next up (M1 continuation): the real-pod path, now that the control-plane
      spine runs end to end —
      - [x] **`cri` (CRI-gRPC to containerd) — implemented + live-smoked.**
        Decisions D9 (containerd/CRI-gRPC) + D10 (youki OCI, deferred to node
        image; M1 runs on runc). `internal/cri` connects, pulls images, creates
        sandboxes with the correct systemd cgroup parent. Live smoke against
        `kindest/node:v1.31.2` (harness `hack/crismoke.go`) surfaced and fixed
        the systemd-cgroup-parent requirement, and located two node-bring-up
        requirements (fuse-overlayfs snapshotter, CNI-config-before-sandbox) —
        recorded in `PLATFORM.md` M4. Added `k8s.io/cri-api` + `grpc` deps.
      - [x] **`cni` + minimal `kubelet` — DONE. A pod actually runs now.**
        M1 networking model: pod networking is done by containerd's CRI plugin
        driving the CNI conflist `neko up` installs — the kubelet does NOT build
        veth/netns by hand (that fights containerd). So `cni` shrank to owning
        the conflist (`cni.Conflist`/`ConflistPath`, reused by `neko up` — dedup);
        the hand-rolled `Network`/`ipam` veth path was removed as the wrong
        abstraction for the CRI model. `kubelet` watches Pods bound to this node,
        creates the sandbox (containerd+CNI assign the IP via `cri.SandboxIP`),
        pulls+starts containers, writes `Running`+PodIP back with CAS retry, and
        tears down on delete. `node-agent` now wires kubelet + a trivial binder
        (placement D5: bind unbound Pods to this node) when a CRI endpoint is
        reachable (API-only otherwise).
      - [x] **M1 GOAL MET — end to end verified:** `neko up` → `neko apply
        examples/pod.toml` → pod reaches **`phase=Running ip=10.244.0.2`** with a
        **real nginx:alpine container running** in the node (crictl: sandbox
        Ready, container Running); `neko delete` tears it down (0 containers).
      - [ ] `neko up`/`down` — DONE (see D11 above).
      - [x] **`neko up`/`down` — implemented + working via kind provisioning (D11).**
        `internal/node` uses kind's Go library to provision the kindest/node
        container (stopping before kubeadm), installs the CNI conflist, copies a
        linux `neko` binary in, and launches `neko node-agent`. **Verified end to
        end:** `neko up` (~8s) → agent healthy on the published host port →
        `neko apply`/`get` round-trip host→node → **CRI smoke passes on our own
        node** (`CRI SMOKE OK`: sandbox→container→teardown). Added `sigs.k8s.io/kind`.

### Known blocker (M4) — RESOLVED via kind provisioning (D11)
**Resolved 2026-10-31.** `neko up` now delegates node provisioning to kind's Go
library (`cluster.Create` + `CreateWithStopBeforeSettingUpKubernetes(true)`):
kind builds the kindest/node container correctly (the overlay/containerd/mount
setup our hand-rolled `docker run` couldn't replicate) and stops before kubeadm;
we install the CNI conflist, copy the `neko` binary in, and launch
`neko node-agent`. **Verified end to end:** `neko up` (~8s) → agent healthy on
the published host port → `neko apply examples/pod.toml` → Pod created, AND the
CRI smoke (`hack/crismoke.go`) now prints `CRI SMOKE OK` on our own node
(sandbox→container→teardown). The container runtime path works.

Tracked follow-up (not required for M1): remove the kind dependency with a
self-contained bring-up once the overlay-snapshotter provisioning gap is
understood. Diagnosis + disproven hypotheses retained below for that effort.

### Historical diagnosis (kept for the self-contained-bringup follow-up)
- **A CRI sandbox's pause task won't stay running in our bare-booted node.**
  `RunPodSandbox` creates the sandbox record, CNI succeeds (netns + IP
  `10.244.0.2` confirmed in containerd debug logs), but the pause **task** exits
  immediately → sandbox `NotReady`, pid 0 → `StartContainer` can't join its
  namespaces.
- **Diagnosis (thorough, 2026-10-31):**
  - Reproduces identically with `crictl runp` (containerd's own reference
    client) — so it is **NOT our Go code**.
  - `ctr run` of the **same pause:3.10 image** works fine → runtime, runc,
    snapshotter (fuse-overlayfs), image, and arch are all healthy.
  - Ruled OUT the cgroup driver: fails the same under both `SystemdCgroup=true`
    (systemd slice created + active) and `SystemdCgroup=false` (cgroupfs).
  - Ruled OUT cgroup-parent format, namespace options, missing container command.
  - **Conclusion:** the gap is specific to the containerd **CRI-plugin shim path**
    in this bare-booted node. `ctr` (direct) works; CRI (shim) doesn't. A real
    `kind create cluster` configures the node beyond booting the container
    (kubeadm-level setup); our `neko up` boots the container correctly but stops
    short of that. This is a **node-provisioning gap, not a neko-box code bug.**
- **Next step (focused):** compare a *working* CRI node (spin up a real
  `kind create cluster`, confirm `crictl runp` works there) against ours, and
  diff the containerd/CRI config + node setup. The delta is the fix for
  `neko up`. If the delta is large, consider: run the agent's kubelet so it does
  the sandbox setup kubelet-style, or build a purpose-made node image.

### Update 2026-10-31 (deep dive) — code CONFIRMED correct; blocker narrowed
- **Our `cri` code works end to end** on a real kind node (`kindest/node:v1.33.1`,
  containerd v2.1.1): `hack/crismoke.go` ran sandbox→container→teardown and
  printed `CRI SMOKE OK`. So the problem is 100% our own node bring-up.
- **Bumped `neko up` default image to `kindest/node:v1.33.1`** and matched kind's
  docker flags exactly (verified by inspecting the user's working kind node:
  privileged, cgroupns=private, seccomp/apparmor/**label=disable**,
  tmpfs /run+/tmp, only /lib/modules bind; **no** forced fuse-overlayfs, **no**
  /dev/fuse, **no** /var volume — all of which I had wrongly added).
- **On our own `neko up` node the CRI sandbox still fails** at the overlay
  snapshot rootfs mount: `failed to mount rootfs component: invalid argument`.
- **Hypotheses tested and DISPROVEN** (don't repeat): cgroup driver (fails under
  both systemd & cgroupfs); image/containerd version (v1.33.1/containerd-2.1.1
  same as working node); layer depth (single-layer pause also fails); manual
  `mount -t overlay` works (incl. userxattr & metacopy variants); recursive
  read-only (rro) mounts — making sandbox root writable did NOT help.
- **Residual cause:** some difference in how containerd's overlayfs *snapshotter*
  mounts inside our bare-booted container vs a kind-*provisioned* one. A manual
  overlay mounts fine; containerd's snapshot overlay mount returns EINVAL.
- **Recommended path forward:** since the code is proven and the gap is purely
  node provisioning, strongly reconsider the parked option — let `kind` (or
  kind's provisioning steps) create the node and run `neko node-agent` inside it
  — rather than keep reverse-engineering kind's overlay bring-up by hand.

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
