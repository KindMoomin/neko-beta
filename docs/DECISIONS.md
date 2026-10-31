# neko-box — Decision Log

Status: **design phase** (no implementation committed yet). This file records the
locked-in choices and the reasoning, so the design doc and later code don't
re-litigate them.

## What neko-box is

A lightweight, local, Kubernetes-**shaped** container runtime for developer
laptops — the spirit of `kind`/`minikube`, but aggressively slimmed for
small-RAM machines. It is **not** a conformant Kubernetes distribution and does
not try to be. It keeps the familiar object *shapes* (Pod, Deployment, Service,
Namespace, ReplicaSet, EndpointSlice) and the apply/get/logs workflow, and
discards everything that exists only to serve production, multi-tenant,
distributed, or arbitrary-client use cases.

Target host: **macOS arm64**. Like kind, each "node" is a privileged Linux
container (run via Docker/containerd); anything that needs the Linux kernel
(netns, veth, cgroups, eBPF) runs inside that container.

## Locked decisions

| # | Decision | Choice | Why |
|---|----------|--------|-----|
| D1 | Userspace language | **Go** for everything in userspace, **except** the eBPF programs and their loader (C/Rust) | I/O-bound HTTP + state-machine code; best containerd/CRI ecosystem; fast iteration; one language for server + `nekoctl`. *Amended (interrogation B1/B2): neko-box owns its BPF programs, so a real C/Rust build re-enters — Go is no longer the only userspace.* |
| D2 | Kernel dataplane | **neko-box owns all its BPF.** A C/Rust `BPF_CGROUP_INET4_CONNECT` LB program + `neko-net`'s `sock_ops` RTT program **vendored as a compiled `.o`** that neko-box loads, attaches, and drains itself | **Amended (interrogation B1/B2).** Original "reuse neko-net's running daemon / open its pinned ringbuf" is impossible: neko-net pins nothing and a ringbuf is single-consumer. neko-box is the sole loader/consumer; neko-net ships as a build artifact, not a process. |
| D3 | API & ecosystem | **neko-native protocol (TOML objects), `nekoctl`-only wire** — NOT kubectl-compatible — **plus a one-way `neko import` translator** that runs `helm template` / parses k8s YAML and converts the supported subset into neko-native and applies it | **Amended (interrogation B3/B4).** Dropping the wire protocol buys little RAM on its own (the real win is D4/monolith, which k3s-style compat designs also get). So instead of choosing between "own protocol" and "compatible", we keep the lean native wire *and* ingest existing artifacts via translation. Live k8s-API proxy (kubectl/helm point at neko-box directly) is a **documented future nice-to-have**, not now. |
| D4 | State storage | **In-process store, no etcd, no storage daemon** — live in-memory objects + append-only WAL (or embedded bbolt) for durability; watches via in-process pub/sub; monotonic revision + optimistic concurrency | etcd's Raft/consistency/distribution is wasted on a single-node disposable local cluster. **This is the decision that actually carries the RAM win** (no etcd, one copy of state). Also unlocks declared-state snapshot/restore. |
| D5 | Scheduler | **Cut** — trivial placement (single node ⇒ here; multi-node ⇒ least-pods/round-robin) | Bin-packing/affinity/taints/spreading solve a multi-node production problem we don't have. ~40 lines replaces it. |
| D6 | Service load-balancing (kube-proxy) | **Cut and replaced by a `BPF_CGROUP_INET4_CONNECT` connect-hook LB program** that rewrites ClusterIP connections to a backend at `connect()` time, reading a `svc_lb` map a Go controller programs from EndpointSlices | **Amended (interrogation B1).** Original "`sock_ops` map lookup" was a category error — `sock_ops` can't rewrite a destination. Connect-time DNAT is how Cilium does kube-proxy replacement. The LB program is a *separate* BPF object from the RTT program and must be C/Rust-compiled (see D1/D2). |
| D7 | Snapshot/restore scope | **Declared-state only** — snapshot = objects + WAL + `svc_lb` map contents; restore re-declares and lets the kubelet re-converge | **New (interrogation M1).** A store/WAL snapshot cannot capture running container processes, open connections, or netns state. We scope the feature honestly as "reset to a known set of apps / reproducible *declared* setups", not live-process checkpoint. CRIU-based live checkpoint is explicitly out of scope (fragile; separate workstream if ever wanted). |
| D8 | Service DNS | **Embed CoreDNS as a library + a custom store-backed `neko` plugin**; the stock `kubernetes` plugin is NOT reused | **New.** CoreDNS's `kubernetes` plugin requires a real client-go apiserver (which D3 removes), so a custom backend plugin is needed regardless. Embedding buys DNS-spec correctness (EDNS/caching/SRV/negative answers) for a one-time ~15–30 MB in-process cost — no second process, consistent with the monolith thesis. Behind a `dns.Resolver` seam so a lighter hand-rolled resolver stays a fallback. See `PLATFORM.md`. |
| D9 | Container runtime | **containerd via CRI-gRPC** (`k8s.io/cri-api`) as the high-level runtime | **New.** Already bundled in `kindest/node`; the best-tested nested-container path (kind proves it); typed Go stubs. The `internal/cri` package implements the CRI client. Alternatives weighed: CRI-O (leaner but needs a custom image + less-trodden nested path), containerd-native/ttrpc (no CRI tax but hand-rolls what CRI gives), DIY-OCI (rebuilds containerd). CRI-gRPC is the lowest-risk path to a running pod. |
| D10 | OCI (low-level) runtime | **youki** (Rust) as the OCI runtime containerd invokes, in place of runc | **New.** Drop-in for runc (OCI-compatible), thematically on-brand with neko-box's Rust eBPF dataplane. Orthogonal to D9: our Go `cri` code is identical; youki is selected via containerd's `runtime_type`/binary config in the node image. runc stays the fallback if youki's nested-container maturity bites. crun was the other contender (leaner C rewrite); youki chosen for the Rust alignment. |
| D11 | Node provisioning | **Delegate to kind's Go library** (`sigs.k8s.io/kind/pkg/cluster`) with `CreateWithStopBeforeSettingUpKubernetes(true)` — kind provisions the kindest/node container correctly, STOPS before kubeadm; neko-box runs its own control plane inside | **New (after a deep dive proved the gap).** Our `cri` code is correct (verified: a full sandbox→container→teardown smoke passes on a kind-provisioned node), but a hand-rolled `docker run` node could not get containerd's overlay snapshotter to mount a container rootfs nested — a node-provisioning gap kind has already solved. So kind is a **node-provisioner, not a Kubernetes distribution**: we skip its kubeadm entirely and stand up neko's own API/store/kubelet in the node. Dependency **accepted for M1**; a self-contained bring-up (removing kind) is a tracked goal, not required now. |

## Cut / slim / defer list (vs stock Kubernetes)

**Deleted outright:** etcd, kube-scheduler, cloud-controller-manager, API
aggregation layer, OpenAPI publishing, admission webhooks, conversion webhooks,
strategic-merge/server-side-apply/managed-fields, audit logging,
encryption-at-rest, most authz modes. (These stay cut regardless of D3 — they
are heavy machinery a single-user disposable local box doesn't need. Note,
post-interrogation: cutting them is worth real *code/complexity*, but only
single-digit MB of *RAM* — the RAM win is D4, not the wire protocol.)

**Replaced:** kube-proxy → a `BPF_CGROUP_INET4_CONNECT` connect-hook LB program
(D6), not deleted — Service LB still has to happen, just in eBPF at connect time.

**Slimmed hard:** controller-manager → keep only the controllers that make pods
actually run (Deployment/ReplicaSet, Service/EndpointSlice, Namespace, maybe
Job); drop node-lifecycle, PV/PVC binding, SA-token rotation, resourcequota,
HPA, etc. kubelet → minimal "node agent" speaking CRI to containerd + our CNI,
with cAdvisor/device-plugins/topology-manager off.

**Deferred (add only when asked):** CSI/volumes, CoreDNS (or a tiny
userspace/eBPF `*.svc.cluster.local` stub), Ingress, NetworkPolicy,
metrics-server, PodSecurity.

## Standout features (design hooks now, build later)

1. **Declared-state cluster snapshot/restore** (D7) — because state is
   in-process + WAL, snapshotting the *declared* cluster (all objects + the
   `svc_lb` map) and restoring it is a fast, consistent file operation; the
   kubelet then re-converges the containers from spec. Great for "reset to a
   known set of apps" and reproducible *declared* setups and demos.
   **Not** a live-process checkpoint — running containers are re-run, not
   frozen/thawed (see D7; CRIU is out of scope).
2. **Built-in per-pod / per-service latency observability** — sourced from the
   vendored `neko-net` RTT `sock_ops` program that **neko-box itself loads and
   drains** (D2), enriched against the object store (resolve `10.244.x.y:port`
   → `svc/...` and the backing pod). Zero-setup latency baked into the dataplane.
3. **`neko import`** (D3) — ingest existing Kubernetes YAML and Helm charts
   (`helm template` → parse → convert the supported subset → apply as
   neko-native). Lets people run artifacts they already have without a
   kubectl-compatible wire protocol.

## The eBPF dataplane (neko-box owns it — D1/D2/D6)

neko-box ships and loads **two** BPF programs itself; neko-net is a vendored
build input, not a running process:
- **CNI (pod networking):** veth + netns + IPAM (Go userspace + netlink).
- **Service LB (kube-proxy replacement):** a **`BPF_CGROUP_INET4_CONNECT`**
  connect-hook program (C/Rust) that rewrites ClusterIP `connect()`s to a
  backend from the `svc_lb` map (+ `GETSOCKNAME` fix-up so apps still see the
  VIP). A Go controller programs the map from EndpointSlices.
- **Observability:** neko-net's `sock_ops` RTT program, compiled to a `.o`,
  loaded and ring-drained by neko-box.

## Open questions (tracked in the risk register, ARCHITECTURE.md)

Most are now **resolved** — see `PLATFORM.md` (platform floor verified on this
machine, node image, cgroup driver, CIDRs, CRI-gRPC, DNS, WAL fsync). Remaining:

- **neko-net payload gap (external dep, still open):** today's `RttEvent` is only
  `daddr/dport/srtt_us`; per-pod/per-service rollups want src addr/port +
  timestamp. Depends on neko-net's own M1.5. (risk M5b)
- WAL vs. embedded bbolt for the durability layer (WAL chosen for M1; bbolt an
  alternative backing that doesn't change the `Store` surface).
- How `nekoctl` addresses the node container's HTTP endpoint on macOS (port map).
- DNS: **embed CoreDNS + custom store-backed `neko` plugin** (D8).

## Non-goals (explicit)

- Conformance with upstream Kubernetes.
- A **live** k8s-API wire that kubectl/helm/operators talk to directly
  (documented as a *future* nice-to-have on top of the `neko import` translation
  core; not in current scope).
- HA / multi-master / surviving node loss.
- Running real production workloads or untrusted/multi-tenant isolation.
- Live process/container checkpoint (CRIU).
