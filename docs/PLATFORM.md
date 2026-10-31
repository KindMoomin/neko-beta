# neko-box — Platform floor & resolved risks

Resolves interrogation risks **M3** (eBPF viability), **M4** (node image /
cgroup / CIDR / CRI), **M5a** (DNS), and **m6** (WAL fsync). Where a claim is
backed by a probe on this machine, it's marked ✅ **verified**; where it's a
design decision, it's marked **decided**.

## Verified on this machine (2026-10-30)

Probed by running a privileged container on the Docker Desktop Linux VM:

| Fact | Value | Why it matters |
|------|-------|----------------|
| VM | Docker Desktop, **kernel 6.10.14-linuxkit, aarch64** | Modern; past every BPF floor we need. |
| cgroup | **v2**, `/sys/fs/cgroup` type `cgroup2`, driver **cgroupfs** | Connect-hook attach point exists; cgroupfs (no systemd) is simpler for the node image. |
| BTF | ✅ `/sys/kernel/btf/vmlinux` present (6.2 MB) | CO-RE relocations work; `cilium/ebpf` loads cleanly. |
| bpffs | ✅ mounts at `/sys/fs/bpf` in a privileged container | Map/program pinning works. |
| `cgroup_sock_addr` program type | ✅ available (bpftool v7.3.0 feature probe) | **The LB hook (`BPF_CGROUP_INET4_CONNECT`) is loadable** — B1's fix is feasible here. |
| `sock_ops` program type | ✅ available | neko-net's RTT program loads. |
| Kernel config | `BPF_SYSCALL`, `CGROUP_BPF`, `BPF_LSM`, `NET_CLS_BPF`, `DEBUG_INFO_BTF`, `BPF_JIT` all `=y` | Every feature the dataplane needs is compiled in. |
| Host network CIDRs in use | `172.17–172.22/16`, `192.168.49/24` (kind/k3d/minikube) | Our pod/service CIDRs must avoid these. |

## M3 — Platform floor (decided, from the probes)

neko-box's dataplane requires, inside the node container:
- **Kernel ≥ 5.8** (connect4 hook since 4.17; `getsockname4` fix-up + split
  `CAP_BPF` since 5.8). This machine: 6.10 ✅.
- **BTF** at `/sys/kernel/btf/vmlinux` (CO-RE). ✅
- **cgroup v2** at `/sys/fs/cgroup`. ✅
- **bpffs** mountable at `/sys/fs/bpf`. ✅
- CONFIG: `CGROUP_BPF`, `BPF_SYSCALL`, `BPF_JIT`, `DEBUG_INFO_BTF`. ✅
- Caps: `CAP_BPF` + `CAP_NET_ADMIN` (or `--privileged`). ✅ via privileged.

**`neko up` preflight** runs these exact probes and **fails loudly** with the
specific missing item before creating a cluster — so a user on an older colima/
Rancher kernel without BTF gets "neko-box needs BTF (kernel CONFIG_DEBUG_INFO_BTF);
your VM kernel 5.x doesn't have it" rather than a cryptic load failure at M2.

**Open (not verifiable without the real program):** the exact **cgroup to attach
the connect-hook to**. Inside the node container, `/sys/fs/cgroup` is the
container's own cgroup; pod sandboxes are child cgroups under it. The LB hook
must attach at a cgroup that is an ancestor of all pod cgroups — the node
container root cgroup works and scopes exactly the pods we run (nothing else
shares this container's cgroup). This is a design choice to validate when the
program first loads, not a platform risk.

## M4 — Node image, cgroup driver, CIDR, CRI protocol (decided)

> **Verified by live smoke (2026-10-31)** against `kindest/node:v1.31.2` +
> containerd v1.7.18. The `internal/cri` client connects, pulls images, and
> creates sandboxes correctly. The smoke harness is `hack/crismoke.go`
> (`//go:build ignore`; build for linux/arm64, copy into a node, run). Three
> concrete nested-container requirements surfaced — each is a **node bring-up**
> concern (what `neko up` must do), not a `cri` code concern:
>
> 1. **Snapshotter must be `fuse-overlayfs`** (or `native`) when nested —
>    overlayfs-on-overlayfs fails ("failed to mount rootfs component"). kind's
>    node entrypoint auto-selects this; `neko up` must too, e.g. set
>    `KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER=fuse-overlayfs` or run the node's
>    own entrypoint rather than a bare `docker run`.
> 2. **Cgroup parent must be systemd-slice form** (`*.slice`) because the node
>    runs containerd with `SystemdCgroup=true` — a plain cgroupfs path makes runc
>    reject the task. **Fixed in `cri.CreateSandbox`** (sets `Linux.CgroupParent`).
> 3. **A CNI config must exist** in `/etc/cni/net.d` before any sandbox starts
>    ("cni plugin not initialized" otherwise). kindest/node ships `ptp`,
>    `host-local`, `loopback`, `portmap` (NOT `bridge`); the `neko` CNI installs
>    its own conflist. Pod CIDR `10.244.0.0/16`.
>
> **Key lesson:** `neko up` cannot be a bare `docker run kindest/node` — that
> skips kind's entrypoint node-setup (snapshotter select, mounts, a stable pause
> process). `neko up` must either drive the kindest/node entrypoint the way kind
> does, or build our own node image. A residual pause-process instability under
> bare-run confirmed this is a bring-up problem, not a CRI-client problem.

- **Node base image:** start from `kindest/node` (it already bundles containerd
  + runc + the nested-container mounts kind spent years getting right) OR a
  minimal Debian + containerd image. Decision: **reuse `kindest/node` for M1**
  to de-risk the nested-containerd integration; revisit a slimmer custom image
  once the spine works. (This directly retires M4's "biggest integration risk".)
  Verified `kindest/node:v1.31.2`: containerd **v1.7.18** with its CRI socket at
  `/run/containerd/containerd.sock`, runc present, systemd as PID 1.
- **cgroup driver:** `kindest/node` runs **systemd** and sets containerd's
  `SystemdCgroup = true` (verified). So the kubelet must use the **systemd**
  cgroup driver to match — NOT cgroupfs. (The VM kernel is cgroup **v2**
  `cgroup2fs`; "v2" is the hierarchy, "systemd" is the driver — don't conflate
  them. Earlier note said cgroupfs; corrected after probing the real image.)
  The classic failure is a driver *mismatch* between kubelet and containerd, so
  we align both on systemd.
- **OCI runtime (D10 youki):** NOT present in stock `kindest/node` (only runc).
  **M1 runs on the image's runc**; swapping in youki is a custom-node-image task
  (install the youki binary + point containerd's `runtime_type`/`BinaryName` at
  it) deferred to the node-image milestone. Our Go CRI code is unaffected.
- **Pod CIDR `10.244.0.0/16`, Service CIDR `10.96.0.0/12`** — both free on this
  host (no `10.x` Docker networks; the collisions are all in `172.16/12` and
  `192.168.49/24`). `neko up` preflight also scans `docker network` and warns if
  a future collision appears.
- **CRI protocol: gRPC via `k8s.io/cri-api`** (not ttrpc). Matches the ecosystem
  and the containerd CRI plugin's native surface; ttrpc is containerd's internal
  services and a bigger, wrong surface.

## M5a — DNS (decided: embed CoreDNS with a custom store-backed plugin)

Services are half-useless without name resolution. **Decision: embed CoreDNS as
a library and compile in a custom `neko` plugin** that serves records from the
in-process store. Rationale and mechanics:

- **Why not the stock `kubernetes` plugin:** it is a hard dead-end for us — it
  requires a real Kubernetes **apiserver** (client-go watches on
  Services/EndpointSlices/Pods) and "cannot function without an API server". We
  deliberately have no client-go apiserver (D3). So the k8s integration can't be
  reused; a custom backend plugin is required *regardless* of embed-vs-build.
- **Why CoreDNS anyway (over a hand-rolled resolver):** we get battle-tested
  query parsing, EDNS, caching, correct negative responses, and SRV handling for
  free — the corners a hand-rolled `*.svc` responder gets wrong. One-time
  ~15–30 MB in-process cost (CoreDNS + its dep tree), no second process.
- **Plugin shape** (CoreDNS plugin SDK, verified): a package implementing
  `plugin.Handler` — `ServeDNS(ctx, dns.ResponseWriter, *dns.Msg) (int, error)`
  and `Name() string` — registered via `plugin.Register("neko", setup)` in
  `setup.go`, with the `neko` entry in `plugin.cfg` and the `kubernetes` entry
  **removed**. `ServeDNS` reads the in-process store (Service ClusterIP → A/AAAA;
  headless → pod A records; ports → SRV) and watches store events so records are
  always live. No Corefile authoring for the user — neko-box generates a minimal
  Corefile (just the `neko` plugin + `cache` + `errors`).
- **Embedding mechanics (honest caveat):** CoreDNS documents the *custom-binary*
  path (`plugin.cfg` + `make`), not a clean "import and `Run()`" API. So neko-box
  builds a CoreDNS server with our trimmed plugin set compiled in and drives it
  **in-process** as a goroutine of the node agent (via `coremain`/`dnsserver`
  with a generated Corefile), listening on the cluster DNS IP. It stays one
  binary and one process — consistent with the monolith thesis — but we own a
  vendored CoreDNS dependency tree and a small build-wiring step (the `neko`
  plugin + pruned `plugin.cfg`). This is the accepted cost of the decision.
- **Interface seam:** the node agent depends on a narrow `dns.Resolver` the
  CoreDNS-embed satisfies; a trivial hand-rolled resolver remains a possible
  fallback behind the same seam if the CoreDNS dep tree ever proves too heavy.
- Pods get the cluster DNS IP in `/etc/resolv.conf`, written at CNI setup.

## m6 — WAL fsync policy (decided)

- **Policy:** `Apply` appends the WAL record and `fdatasync`s **before** publishing
  the event to watchers, by default. This guarantees that anything a controller or
  the kubelet *acted on* (e.g. started a container) is already durable — so after a
  crash the store is never *behind* real container state.
- **Escape hatch:** a `--wal-sync=batch` mode (fsync every N ms) for speed on
  throwaway clusters, explicitly trading a small loss window. Acceptable because
  the cluster is disposable (D4) and reconcilers are **level-triggered** — on
  restart, replay + relist re-converges; a lost tail event self-heals.
- **Conformance test (now, per m6):** property-test `Watch` resume / `ErrTooOld`
  compaction / buffer-overflow against the in-proc store, so the future HTTP
  Watcher adapter (M2) has an executable spec to meet.

## Net effect on the milestones

- **M1 unblocked:** platform floor verified, node image chosen (`kindest/node`),
  cgroup driver pinned (cgroupfs), CIDRs chosen (`10.244/16` + `10.96/12`),
  CRI-gRPC committed, fsync policy decided. The preflight is a small M1 task.
- **M2 unblocked on DNS:** embed CoreDNS in-process with a custom store-backed
  `neko` plugin (the `kubernetes` plugin can't be reused — no apiserver). Adds a
  one-time ~15–30 MB to the node agent's RSS and a vendored CoreDNS dep tree —
  worth noting against the ~70–130 MB idle budget (still well under k3s). Behind
  a `dns.Resolver` seam so a lighter hand-rolled resolver stays a fallback.
- **Still genuinely open (external):** M5b — neko-net's richer RTT payload
  (src addr/port + timestamp) is that project's unshipped M1.5; neko-box's first
  latency cut uses today's `daddr/dport/srtt_us` and enriches dst-side only.
