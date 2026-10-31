# neko-box 🐱📦

A lightweight, local, Kubernetes-**shaped** container runtime for developer
laptops — like `kind`/`minikube`, but aggressively slimmed for small-RAM
machines. **Not** a conformant Kubernetes distribution.

> Status: **M1 works — a pod actually runs.** `neko up` provisions a node (via
> kind, no kubeadm), and `neko apply` of a TOML Pod starts a real container with
> a pod IP, reported back through neko-box's own API. The eBPF dataplane
> (Service LB + RTT), Deployments/Services, and DNS are the next steps. See
> [`docs/`](docs/).

## Run a pod

```sh
go build -o neko ./cmd/neko
GOOS=linux GOARCH=arm64 go build -o neko-linux ./cmd/neko   # the in-node binary

# provision a node (kind-provisioned container; neko's own control plane inside)
./neko up --port 7443 --neko-bin ./neko-linux

# author a Pod in TOML and apply it
./neko apply examples/pod.toml       # Pod/hello created
./neko get Pod                       # default/hello  — reaches phase=Running with a podIP
./neko delete Pod hello              # stops the container

./neko down                          # tear the node down
```

`neko apply` → the store → the kubelet creates a sandbox (containerd + CNI
assign the IP) → pulls and starts the container → reports `Running` + `podIP`.


## The idea in one paragraph

Keep the familiar Kubernetes object shapes (Pod, Deployment, Service,
Namespace) and the `apply`/`get`/`logs` workflow. Throw out everything that
only exists to serve production, distribution, multi-tenancy, or arbitrary
API clients — etcd, the scheduler, kube-proxy, the aggregation/OpenAPI/webhook
machinery. Collapse what's left into as few moving parts as possible, in Go.
Use a custom eBPF dataplane (reused from the sibling `neko-net` project) for
pod networking, Service load-balancing, and zero-setup latency observability.

## Design at a glance

- **Userspace: Go** — API server, store, controllers, placement, the `nekoctl`
  client, CNI/IPAM, containerd integration. (The eBPF programs themselves are
  C/Rust — see below.)
- **Kernel dataplane: two eBPF programs neko-box owns and loads itself** — a
  `BPF_CGROUP_INET4_CONNECT` connect-hook for Service load-balancing, and
  [`neko-net`](../neko-net)'s `sock_ops` RTT program **vendored as a compiled
  object** for latency observability.
- **neko-native API protocol** (TOML objects), `nekoctl`-only — not
  kubectl-compatible on the wire. **`neko import`** ingests existing Kubernetes
  YAML and Helm charts (one-way: `helm template` → convert → apply).
- **No etcd**: state lives in-process with a WAL for durability.
- **No scheduler, no kube-proxy**: trivial placement; Service LB is the eBPF connect-hook.
- **Node = a privileged Linux container** (kind-style), so it runs on macOS.

## Standout features (planned)

- **Declared-state cluster snapshot/restore** (objects + WAL) — reset to a known
  set of apps / reproducible setups. *Not* a live-process checkpoint.
- **Built-in per-pod/per-service latency** from the eBPF RTT stream.

## Docs

- [`docs/DECISIONS.md`](docs/DECISIONS.md) — locked decisions + rationale (D1–D7).
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — full design, module map,
  interface sketches, pod lifecycle, RAM budget, milestones, risk register.
- [`docs/PLATFORM.md`](docs/PLATFORM.md) — platform floor (verified on this
  machine), node image, cgroup/CIDR/CRI, DNS, and WAL decisions.
- [`docs/INTERROGATION.md`](docs/INTERROGATION.md) — adversarial review findings
  that reshaped the design.
- [`docs/PROGRESS.md`](docs/PROGRESS.md) — what's done / next.

## Layout (planned)

```
cmd/neko        the binary (node-agent role + nekoctl)
internal/       store, access, api, httpapi, controllers, placement, kubelet,
                cri, cni, dataplane, nekonet, observ, importer, snapshot
bpf/            NON-Go eBPF programs: svc_lb (connect-hook LB) + rtt (vendored neko-net)
images/node     the node-container base image
docs/           design & progress
```
