# neko-box — Architecture

> Synthesized from two competing whole-system design sketches (pstack architect
> Phase B, `opus high` ×2): **A** a single-process monolith, **B** cleanly
> separated processes dogfooding the public API. This document is the chosen
> shape. See `DECISIONS.md` for the locked constraints it works within.

## Synthesis decision

The two variants agreed on almost everything that matters — the object model,
the store's revision + optimistic-concurrency + in-process pub/sub watch, the
eBPF Service-LB boundary, the neko-net RTT ingestion boundary, and the
`nekoctl apply → running pod` pipeline are nearly identical in both. They
diverged on exactly **one axis**: where the kubelet and controllers obtain
their access to the store.

- **Variant A (monolith):** controllers + kubelet are goroutines holding a
  `*Store` pointer; they call it by Go function call. Idle control-plane RSS
  **~70–130 MB**. Weakness: one crash domain; multi-node looks like a rewrite
  unless the store-access seam is drawn deliberately.
- **Variant B (separated processes):** apiserver, kubelet, controllers are
  distinct processes; all reach state over the *same* HTTP+watch contract that
  `nekoctl` uses. Idle RSS **~125–220 MB** (≈3 Go runtimes). Weakness: ~20–35 MB
  tax and cross-process watch correctness becomes load-bearing — paid even
  single-node, which is the common case.

**Chosen: the monolith (A), with B's contract drawn as a narrow seam.** The
project's north star is small-RAM laptops, and A is 2–3× lighter at idle. But
we adopt B's central insight without its cost by making the kubelet and
controllers depend not on `*Store` directly but on a **narrow
`Reader`/`Watcher`/`Writer` access interface**. In a single node that interface
is satisfied *by the in-process store itself* (zero overhead, A's RAM profile).
When a second node appears, the remote node runs only the kubelet and satisfies
the same interface with an **HTTP client** against the primary's API — which is
exactly Variant B's contract, materialized only where it's actually needed.

This is the deepest shape available: one tiny interface hides the
"local vs remote state" question. The **Reader/Writer** halves (Get/List/Apply/
Delete) translate to HTTP cleanly and are genuinely unchanged above the seam.
The **Watcher** half does **not** translate for free (interrogation M2): in-proc
watch is an ordered, never-partial Go channel, while HTTP/SSE watch adds
disconnects, reconnect gaps, and mid-stream `ErrTooOld`. Level-triggering means
a *missed event* self-heals — but only if something triggers a relist, and that
reconnect/resync/backoff logic lives in the HTTP **adapter**, which the in-proc
impl doesn't have. So the honest claim is: *Reader/Writer unchanged; the Watcher
adapter owns reconnect/resync so controllers stay level-triggered.* We validate
this early with a loopback-HTTP Watcher run against the real controllers (see
risk register), rather than discovering the leak at multi-node.

It also respects the global rule *no interface without ≥2 real implementations*
— with one honest caveat:
- `Store` stays a **concrete struct** — it has exactly one implementation.
- The store-**access** interface currently has **one** implementation (`InProc`)
  justified by a *promised* second (the HTTP client). That is the one place we
  build an interface ahead of its second impl; we mitigate by writing the
  loopback-HTTP Watcher in M1/M2 so the second impl exists and validates the
  seam early, instead of staying speculative until multi-node.
- `Controller` is an interface because it has 5 implementations.
- `cri`, `cni`, `dataplane`, `nekonet` stay concrete structs until a test needs a fake.

> **Language note:** Go is the userspace language, but **not the only** language.
> The two eBPF programs neko-box owns — the `svc_lb` connect-hook LB and the
> vendored neko-net RTT `sock_ops` — are C/Rust compiled to `.o` under `bpf/`
> (interrogation B1/B2). Go loads/attaches them and writes their maps.

## Topology

```
┌──────────────────────────── macOS arm64 HOST ────────────────────────────┐
│                                                                           │
│   nekoctl  (the `neko` binary, CLI role)                                  │
│     │  HTTP+JSON (apply/get/delete) + GET /watch (SSE/NDJSON)             │
│     │  via published node-container port  127.0.0.1:PORT → node:7443      │
│     ▼                                                                     │
│  ┌──────────── NODE CONTAINER (privileged Linux, arm64; kind-style) ───┐  │
│  │                                                                     │  │
│  │   ONE PROCESS:  `neko node-agent`                                   │  │
│  │   ┌─ httpapi ─┐   (the ONLY external/serialization seam)            │  │
│  │   │ REST+SSE  │◀───── nekoctl                                       │  │
│  │   └────┬──────┘                                                     │  │
│  │        │ in-process Go calls                                        │  │
│  │   ┌────▼──────────────── store (concrete) ───────────────┐          │  │
│  │   │ in-mem objects + monotonic revision + pub/sub + WAL   │          │  │
│  │   └──▲────────▲────────▲──────────▲────────────────────┘          │  │
│  │      │ StoreAccess (Reader/Watcher/Writer) — in-proc impl          │  │
│  │   ┌──┴──┐ ┌───┴───┐ ┌──┴───┐ ┌────┴─────┐  (goroutines)            │  │
│  │   │depl/│ │ svc/  │ │ ns   │ │ kubelet  │                          │  │
│  │   │ rs  │ │ eps   │ │ ctrl │ │  -equiv  │                          │  │
│  │   └─────┘ └──┬────┘ └──────┘ └─┬────────┘                          │  │
│  │              │ eBPF map        │ CRI (ttrpc)  │ netns/veth (netlink)│  │
│  │        ┌─────▼────┐      ┌─────▼─────┐   ┌────▼────┐                │  │
│  │        │ dataplane│      │ cri /     │   │ cni /   │                │  │
│  │        │ (svc_lb  │      │ containerd│   │ ipam    │                │  │
│  │        │  writer) │      │  client   │   │         │                │  │
│  │        └────┬─────┘      └─────┬─────┘   └────┬────┘                │  │
│  └─────────────┼──────────────────┼──────────────┼────────────────────┘  │
│                ▼ node-container Linux kernel      ▼                        │
│   ┌───────────────────────────────────────────────────────────────────┐  │
│   │ eBPF DATAPLANE (kernel):                                           │  │
│   │  • neko-net sock_ops RTT program + ringbuf  (Rust/aya, EXTERNAL)   │  │
│   │  • svc_lb map: ClusterIP:port → [backend]   (Go writes, kernel LB) │  │
│   │  containerd/runc → pod sandboxes (netns + veth pairs)              │  │
│   └───────────────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────────────┘
```

The only HTTP/serialization arrow is `nekoctl → httpapi`. Everything below
`httpapi` is a Go call or a syscall (bpf / ttrpc / netlink / ringbuf read).

**Multi-node (post-M2):** each extra node is another container running *only*
the kubelet + cni/cri/dataplane + neko-net, whose `StoreAccess` is an HTTP
client pointed at node 0's `httpapi`. The store, controllers, and placement are
singletons on node 0. No code above `StoreAccess` changes.

## Module map (Go)

Module `github.com/neko-box/neko`; `internal/` because none of it is a public library.

```
cmd/neko/                 main(); subcommand → role (node-agent | nekoctl aliases | up/down)

internal/api/             object model: Pod, Deployment, ReplicaSet, Service,
                          EndpointSlice, Namespace, Job; ObjectMeta, TypeMeta.
                          Single version "neko/v1", no conversion. JSON tags only.

internal/store/           THE store, concrete struct: Get/List/Apply/Delete/Watch,
                          monotonic revision, optimistic concurrency, in-proc pub/sub.
internal/store/wal/       append-only durability + replay + snapshot/restore hooks.
internal/access/          StoreAccess (Reader/Watcher/Writer) interface + the
                          in-process adapter over *store.Store. (HTTP adapter: M-multinode.)

internal/httpapi/         the only external seam: REST mux + SSE watch; marshals HTTP
                          verbs → StoreAccess. Only package besides access/ that the
                          external world reaches.

internal/controller/      Controller interface + shared run loop (watch→workqueue,
                          per-key rate limit, recover() per reconcile).
internal/controller/deployment/   Deployment → ReplicaSet.
internal/controller/replicaset/   ReplicaSet → Pods (+ placement).
internal/controller/service/      Service + ready Pods → EndpointSlice AND svc_lb map.
internal/controller/namespace/    namespace lifecycle / cascade delete.
internal/controller/job/          (M2+) Job → Pods.

internal/placement/       Pick() — pure; single-node → here, multi-node → least-pods.
internal/kubelet/         node agent: watch Pods bound here → cni → cri → status write.
internal/cri/             containerd CRI client (gRPC, k8s.io/cri-api): pull, sandbox, container.
internal/cni/             netns + veth + bridge; the pod-network setup boundary.
internal/cni/ipam/        host-local CIDR allocator (rebuildable from live Pods).
internal/dataplane/       svc_lb map WRITER (Go/cilium-ebpf) for the connect-hook LB program.
internal/nekonet/         loads+attaches the vendored BPF objects (RTT + LB), drains the RTT ringbuf.
internal/observ/          per-pod/per-service RTT rollups; read-only via httpapi.
internal/dns/             dns.Resolver seam + embedded CoreDNS driver; the `neko`
                          CoreDNS plugin's ServeDNS reads Services/EndpointSlices
                          from the store (stock `kubernetes` plugin unusable — no apiserver).
internal/importer/        `neko import`: helm template / parse k8s YAML → neko-native TOML → apply.
internal/snapshot/        declared-state snapshot/restore (objects + WAL + svc_lb map). NOT live containers.
internal/clientcmd/       nekoctl HTTP client + apply/get/describe/logs rendering (TOML native).

bpf/                      NON-Go. The eBPF programs neko-box owns and compiles:
  bpf/svc_lb/             BPF_CGROUP_INET4_CONNECT connect-hook LB program (C or Rust).
  bpf/rtt/                vendored neko-net sock_ops RTT program, built to a .o.
```

Dependency rule (lint-enforced): only `store/`, `access/`, and `httpapi/`
touch the store internals. Controllers, kubelet, placement, and nekoctl depend
on `access.StoreAccess` + `api/` — never on `*store.Store` directly. This one
rule is what keeps the single→multi-node swap a wiring change.

## Key type & interface sketches

### Object model (`internal/api`)

```go
type TypeMeta struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"` // always "neko/v1"; single version, no conversion
}

type ObjectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	UID       string            `json:"uid"`
	Revision  uint64            `json:"revision"`           // store-assigned; optimistic-concurrency token
	Labels    map[string]string `json:"labels,omitempty"`
	OwnerRefs []OwnerRef        `json:"ownerRefs,omitempty"`
	Deleting  bool              `json:"deleting,omitempty"` // tombstone for cascade delete
}

// Object is implemented by every stored kind. Deliberately tiny.
type Object interface {
	GetMeta() *ObjectMeta
	GetType() TypeMeta
	DeepCopyObject() Object // store never hands out aliased pointers // not implemented
}

type Pod struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}
type PodSpec struct {
	NodeName   string      `json:"nodeName,omitempty"` // set by placement
	Containers []Container `json:"containers"`
}
type PodStatus struct {
	Phase string `json:"phase"` // Pending|Scheduled|Running|Succeeded|Failed
	PodIP string `json:"podIP,omitempty"`
	Ready bool   `json:"ready"`
}
// Deployment / ReplicaSet / Service / EndpointSlice / Namespace share the shape.
```

### The store (`internal/store`) — concrete struct, source of truth

```go
// Store is the single source of truth. Concrete (one impl), thread-safe.
type Store struct {
	// unexported: RWMutex, map[Kind]map[nsName]api.Object, rev uint64, pubsub, wal
}

type Key struct{ Kind, Namespace, Name string }

type EventType int
const ( Added EventType = iota; Modified; Deleted )

type Event struct {
	Type     EventType
	Object   api.Object // post-image; tombstone for Deleted
	Revision uint64
}

func (s *Store) Get(k Key) (api.Object, bool)                        { panic("not implemented") } // returns deep copy
func (s *Store) List(kind, ns string, sel labels.Selector) ([]api.Object, uint64) { panic("not implemented") }

// Apply is the single mutating entry. expectedRevision==0 ⇒ create-only; else CAS
// against current revision (ErrConflict on mismatch). On success: bump the global
// monotonic counter, stamp obj.Meta.Revision, WAL-append, THEN publish the event.
func (s *Store) Apply(obj api.Object, expectedRevision uint64) (api.Object, error) { panic("not implemented") }
func (s *Store) Delete(k Key, expectedRevision uint64) error        { panic("not implemented") }

// Watch is IN-PROCESS pub/sub. sinceRevision==0 ⇒ replay live state as Added, then
// tail. Buffered channel; overflow ⇒ one Error event, consumer must relist (bounded mem).
// A sinceRevision compacted past returns ErrTooOld (forces level-triggered relist).
func (s *Store) Watch(ctx context.Context, kind, ns string, sinceRevision uint64) (<-chan Event, error) { panic("not implemented") }

func (s *Store) CurrentRevision() uint64 { panic("not implemented") }

var ErrConflict = errors.New("revision conflict")
var ErrTooOld   = errors.New("watch revision compacted")
```

WAL: each applied event is a length-prefixed JSON record; boot replays WAL (or
snapshot + tail). `bbolt` is a drop-in alternative backing — the `Store` surface
doesn't change, so that choice stays deferrable.

### The seam (`internal/access`) — the one interface that goes remote

```go
// StoreAccess is the narrow surface controllers + kubelet depend on, so they are
// identical whether state is local (in-proc) or remote (HTTP, multi-node).
// Two real implementations justify the interface: InProc now, HTTP later.
type Reader interface {
	Get(ctx context.Context, k store.Key) (api.Object, bool, error)                       // not implemented
	List(ctx context.Context, kind, ns string, sel labels.Selector) ([]api.Object, uint64, error) // not implemented
}
type Watcher interface {
	// Watch contract (must hold for BOTH the in-proc and the future HTTP/SSE impl):
	// events strictly ordered by rev; sinceRev==0 ⇒ replay live state then tail;
	// a compacted sinceRev ⇒ ErrTooOld (caller relists). The ADAPTER — not the
	// controller — owns detecting disconnect/gap and triggering resync with backoff.
	// Controllers stay level-triggered so a missed event self-heals on the next relist.
	Watch(ctx context.Context, kind, ns string, sinceRev uint64) (<-chan store.Event, error) // not implemented
}
type Writer interface {
	Apply(ctx context.Context, obj api.Object) (api.Object, error) // rev carried on obj.Meta // not implemented
	Delete(ctx context.Context, k store.Key, rev uint64) error                               // not implemented
}
type StoreAccess interface { Reader; Watcher; Writer }

// InProc wraps *store.Store with zero serialization — the single-node fast path.
type InProc struct{ S *store.Store }
// (HTTP adapter implementing StoreAccess over httpapi is added for multi-node.)
```

### Controller runtime (`internal/controller`)

```go
// Controller reconciles one or more kinds. The run loop wakes it with a changed key.
type Controller interface {
	Name() string
	Kinds() []string
	Reconcile(ctx context.Context, key store.Key) error // not implemented
}

// Run wires each controller to Watch, funnels events into per-key rate-limited
// workqueues, and wraps every Reconcile in recover() so one bad key ≠ dead process.
func Run(ctx context.Context, a access.StoreAccess, cs ...Controller) error { panic("not implemented") }
```

Reconcilers are **level-triggered**: each reads full desired+actual via `access`
and converges with CAS `Apply`, so a missed watch edge self-heals on the next
relist. That property is what makes both the per-key `recover()` and the remote
watch-reconnect safe.

### Placement (`internal/placement`) — a function, not a service

```go
type NodeView struct{ Name string; PodCount int; Ready bool }

// Pick: single-node ⇒ the one ready node; multi-node ⇒ least-pods, ties by UID hash.
func Pick(pod *api.Pod, nodes []NodeView) (nodeName string, err error) { panic("not implemented") }
```

### CRI (`internal/cri`), CNI/IPAM (`internal/cni`)

```go
type Runtime struct{ /* containerd client over ttrpc */ }
func (rt *Runtime) EnsureImage(ctx context.Context, ref string) error { panic("not implemented") }
func (rt *Runtime) CreateSandbox(ctx context.Context, pod *api.Pod, netnsPath string) (sandboxID string, err error) { panic("not implemented") }
func (rt *Runtime) StartContainer(ctx context.Context, sandboxID string, c api.Container) (id string, err error) { panic("not implemented") }
func (rt *Runtime) Stop(ctx context.Context, sandboxID string) error { panic("not implemented") }

type PodNet struct{ NetnsPath string; IP netip.Addr; MAC net.HardwareAddr }
type Network struct{ /* bridge/veth config + *ipam.Allocator */ }
func (n *Network) Setup(ctx context.Context, pod *api.Pod) (PodNet, error) { panic("not implemented") }
func (n *Network) Teardown(ctx context.Context, pod *api.Pod) error        { panic("not implemented") }

type Allocator struct{ /* CIDR + in-use set; rebuildable from live Pods on boot */ }
func (a *Allocator) Allocate(podUID string) (netip.Addr, error) { panic("not implemented") }
func (a *Allocator) Release(podUID string) error               { panic("not implemented") }
```

### Service LB (`bpf/svc_lb` + `internal/dataplane`) — the kube-proxy replacement

**Mechanism (corrected — see interrogation B1).** A ClusterIP is a virtual IP
with no interface or listener, so you cannot load-balance it from `sock_ops`
(that hook only fires *after* the 4-tuple is chosen and can't rewrite the
destination). The LB is a **`BPF_CGROUP_INET4_CONNECT`** connect-hook program
(+ `INET6_CONNECT`, UDP `SENDMSG`/`RECVMSG`, and `GETSOCKNAME` fix-up) attached
to the pods' cgroup. It runs *inside* `connect()`, looks up the `svc_lb` map,
and rewrites `user_ip4`/`user_port` to a chosen backend before the SYN is built
— socket-level DNAT, exactly how Cilium replaces kube-proxy. `GETSOCKNAME`
rewrites the reverse so apps still observe the ClusterIP.

This program is **not Go and not neko-net** — it's a separate BPF object neko-box
authors and compiles (C or Rust) under `bpf/svc_lb`. Go only *writes the map*:

```go
type Backend struct{ IP netip.Addr; Port uint16 }

// Programmer writes the svc_lb map that the connect-hook LB program reads.
// Diff-based, idempotent. The service controller recomputes the EndpointSlice,
// writes it to the store AND calls SetService — two sinks, one reconcile.
type Programmer struct{ svcMap *ebpf.Map /* the svc_lb map the connect-hook reads */ }
func (p *Programmer) SetService(clusterIP netip.Addr, port uint16, proto uint8, backends []Backend) error { panic("not implemented") }
func (p *Programmer) RemoveService(clusterIP netip.Addr, port uint16, proto uint8) error { panic("not implemented") }
```

### RTT ingestion (`internal/nekonet`, `internal/observ`) — neko-box is the sole loader

**Ownership (corrected — see interrogation B2).** neko-net pins nothing and a
ringbuf is single-consumer, so neko-box cannot "open neko-net's pinned ringbuf"
while neko-net's own daemon runs. Instead **neko-box vendors neko-net's
`sock_ops` program as a compiled `.o` (`bpf/rtt`), loads and attaches it itself,
and owns the ringbuf fd.** neko-net's userspace daemon does not run inside the
node container. `nekonet` also loads/attaches the `svc_lb` connect-hook program
— it is the single BPF loader for the whole node.

```go
// Sample is neko-box's enriched RTT record. The ON-WIRE ringbuf layout is
// neko-net's RttEvent (daddr/dport/srtt_us today); nekonet decodes that fixed
// layout cgo-free and enriches (src, pod identity, timestamp) on the Go side.
type Sample struct {
	SrcIP, DstIP     netip.Addr
	SrcPort, DstPort uint16
	RTTMicros        uint32
	StampNanos       uint64
}

// Dataplane is the single BPF loader/attacher for the node.
type Dataplane struct{ /* loaded collections + ringbuf.Reader for RTT */ }
func (d *Dataplane) Load(ctx context.Context) error                 { panic("not implemented") } // load+attach rtt + svc_lb
func (d *Dataplane) ReadRTT(ctx context.Context, out chan<- Sample) error { panic("not implemented") } // drain OUR ringbuf
func (d *Dataplane) Close() error                                   { panic("not implemented") }

// observ joins samples to pods/services via the IP ownership IPAM+dataplane know,
// keeps bounded p50/p95 histograms, and serves them read-only through httpapi.
type Rollup struct{ /* ring of per-key percentiles */ }
func (ro *Rollup) Ingest(s nekonet.Sample)                        { panic("not implemented") }
func (ro *Rollup) ForService(ns, name string) (LatencyStat, bool) { panic("not implemented") }
```

> **neko-net payload gap (risk, tracked).** neko-net's current `RttEvent`
> (`~/neko-net/neko-net-common/src/lib.rs`) carries only `daddr/dport/srtt_us`
> — no src addr/port, no timestamp, no pod identity. Per-pod/per-service rollups
> want the richer layout; closing that is **neko-net's own unshipped M1.5 work**,
> an external dependency neko-box does not control (see risk register). For the
> first latency cut we consume exactly what neko-net emits today and enrich what
> we can from the store (dst IP → pod/service); src-side attribution waits on M1.5.

### `neko import` — the ecosystem bridge (`internal/importer`)

```go
// Import converts existing Kubernetes artifacts into neko-native objects and
// applies them. One-way: there is NO live kubectl/helm wire (that is a documented
// future nice-to-have, not built). `neko import chart.tgz` runs `helm template`;
// `neko import k8s.yaml` parses manifests. Unsupported kinds are reported, skipped.
func Import(ctx context.Context, a access.StoreAccess, src Source) (Report, error) { panic("not implemented") }
```
The native authoring format is **TOML**, not YAML — `import` is the on-ramp for
people who already have YAML/charts; hand-authored neko objects are TOML.

### HTTP API surface (`internal/httpapi`) — the only external seam

```
GET    /v1/{kind}                         list        → Reader.List
GET    /v1/{kind}/{ns}/{name}             get         → Reader.Get
GET    /v1/{kind}?watch=1&since={rev}     watch (SSE) → Watcher.Watch
POST   /v1/{kind}/{ns}                    create      → Writer.Apply(obj, rev=0)
PUT    /v1/{kind}/{ns}/{name}?rev={rev}   replace     → Writer.Apply(obj, rev)  # 409 on ErrConflict
DELETE /v1/{kind}/{ns}/{name}?rev={rev}   delete      → Writer.Delete
GET    /v1/pods/{ns}/{name}/log           container logs (streams from cri)
GET    /v1/latency/service/{ns}/{name}    observ.Rollup read
POST   /v1/snapshot                       snapshot.Save → blob (DECLARED state: objects+WAL+svc_lb map; NOT running containers)
POST   /v1/restore                        snapshot.Restore(blob) → re-declare; kubelet re-converges containers
GET    /v1/healthz                        ready:true only after WAL replay completes
```

Optimistic concurrency rides on `?rev=` → `Apply`; 409 ⇒ `nekoctl` re-GETs and
retries. No three-way merge, no managed fields, no OpenAPI. That is the entire
concurrency protocol.

## Pod lifecycle: `nekoctl apply` → running container + network

Only steps 1 and the final `get` cross the HTTP seam; everything else is Go
calls and syscalls in the one process.

1. **nekoctl (host):** parse native TOML (or imported YAML) → `api.Pod`; `POST /v1/pods/default`.
2. **httpapi:** decode → `Writer.Apply(pod, 0)` → store assigns UID+revision,
   WAL-appends, publishes `Added`; 201 back. *(Bare Pod skips to 5; a Deployment
   runs 3–4 first.)*
3. **deployment ctrl** (woken by in-proc Watch) → `Apply` a ReplicaSet.
4. **replicaset ctrl** sees the shortfall → builds Pods, `placement.Pick` sets
   `Spec.NodeName`, `Apply`s them. No separate scheduler — placement is a call.
5. **kubelet** (watching Pods bound to this node): `cni.Setup` (netns, IPAM IP,
   veth) → `cri.EnsureImage` → `cri.CreateSandbox` → `cri.StartContainer` →
   `Apply` status `Running` + `PodIP`.
6. **service ctrl** (if labels match a Service & Pod is Ready): recompute
   `EndpointSlice`, `Apply` it, **and** `dataplane.SetService(...)` → writes the
   `svc_lb` eBPF map. ClusterIP now load-balances in-kernel.
7. **dataplane (neko-box-loaded):** pod TCP connections hit the RTT `sock_ops`
   program; `nekonet` drains **our** ringbuf → `observ.Rollup`, joined to
   pod/service by IP (src-side attribution pending neko-net M1.5).
8. **nekoctl get pods** → `GET /v1/pods` → `Running` with `PodIP`;
   `nekoctl latency svc/...` → `GET /v1/latency/...`.

## RAM budget — honest baseline (interrogation B3)

**The real competitor is k3s, not kind.** kind is the heaviest local option
(full upstream apiserver + etcd + separate controller-manager/scheduler/proxy).
But a RAM-conscious user would reach for **k3s**, which runs a *conformant,
fully kubectl/Helm-compatible* server using SQLite-not-etcd and folds the control
plane into one process — the same levers neko-box uses — at **~250–400 MB idle**.

| | kind | k3s (real competitor) | neko-box |
|---|---|---|---|
| Idle control-plane RSS | ~600 MB–1 GB | ~250–400 MB | **~70–130 MB** |
| kubectl/Helm/operators | ✅ | ✅ | ❌ native (✅ via `neko import`, one-way) |
| Multiple over k3s | — | 1× | **~2–3×** |

So the honest win is **~2–3× over k3s**, not 5–8× over kind. Where the savings
actually come from, in order:
1. **No etcd / no second copy of state** (decision D4) — objects live once, as
   Go structs, moved by pointer, never serialized internally.
2. **One Go runtime** instead of several (one GC heap, one scheduler).
3. Scheduler → a function; kube-proxy → connect-hook + map writes.
4. No aggregation / OpenAPI / admission / conversion machinery.

**Crucial caveat (B3/B4):** savings 1–2 are the big ones, and they come from the
**monolith + in-process store (D4)** — which a compat-preserving design like k3s
*also* captures. The saving attributable *specifically* to the neko-native wire
(item 4) is the smallest, single-digit MB. So neko-box does **not** justify
dropping the wire protocol *on RAM grounds* — it justifies it on a leaner,
TOML-native authoring surface plus the two standout features, while `neko import`
keeps the ecosystem reachable. If the ~2–3× over k3s isn't worth losing live
kubectl, that's the signal to build the future live-proxy (documented, D3).

## Milestone 1 — internal spine milestone (NOT a user-facing release)

**This proves the mechanism end-to-end, not the product** (interrogation M5).
It is bare-Pods-only: no Deployments (nobody ships bare Pods), no Services/DNS
(pods can't resolve each other), and neither standout feature — i.e. functionally
"a heavier `docker run`." Ship it as an internal checkpoint, not a release.

**Goal:** `nekoctl apply pod.toml` starts a real container with a real IP in one
node container, and `nekoctl get pods` shows it `Running` with `PodIP`.

**Includes:**
- `api`: `Pod`, `Namespace`, `ObjectMeta` only.
- `store` + `store/wal`: `Get/List/Apply/Delete/Watch` + revision + optimistic
  concurrency, in-memory with WAL append + replay-on-boot (+ documented fsync policy).
- `access.InProc` **and a loopback-HTTP Watcher** run against the controllers to
  validate the watch seam early (interrogation M2) — not deferred to multi-node.
- `httpapi`: create/get/list/watch(SSE)/delete for Pods + `/log` + `/healthz`.
- `controller.Run` loop + a minimal `kubelet` consumer only (watch Pods → cni/cri).
- `placement.Pick` (returns the single node).
- `cri`: image pull + sandbox + single-container start/stop over **CRI-gRPC**
  (`k8s.io/cri-api`) against in-container containerd (interrogation M4 — not ttrpc).
- `cni` + `ipam`: netns + veth + host-local IP from a **pod CIDR chosen to avoid
  Docker's `172.17/16` and the colima network** (M4).
- `cmd/neko`: `node-agent`, `up`/`down` (boot the privileged node container with a
  **preflight** that probes kernel/BTF/cgroup/mounts and fails loudly — M3), and
  `nekoctl apply|get|delete|logs`.

**Explicitly cut from M1 (designed-for, deferred):**
- Deployment / ReplicaSet / Service / EndpointSlice / Job controllers — **M1 runs bare Pods only**.
- eBPF Service LB (the `svc_lb` connect-hook program + `dataplane` writer; no Service yet).
- RTT ingestion + `observ` latency — a standout feature, **M2** (and gated on neko-net M1.5).
- Snapshot/restore endpoints (WAL exists; declared-state `/snapshot` orchestration is M2).
- `neko import` translator (M2+).
- Multi-node + `access` HTTP adapter (but the loopback-HTTP Watcher ships in M1),
  multi-container pods, volumes/CSI, DNS, non-equality selectors, bbolt, authz/TLS.

M1 proves the *mechanism* end-to-end on the smallest object graph: one process,
store-as-truth, in-process kubelet, one HTTP seam, a real networked container.

## Milestone 2 — first user-facing release (interrogation M5)

The smallest thing a developer would actually use:
- **Deployment + ReplicaSet** controllers (real apps, not bare Pods).
- **Service + EndpointSlice** + the **`svc_lb` connect-hook LB** program wired up.
- **DNS** for `*.svc` (CoreDNS vs a small userspace/eBPF stub — an open question,
  see risk register; without it Services are half-useless).
- **At least one standout feature**: RTT latency (`observ`) *or* declared-state
  snapshot/restore (D7).
- **`neko import`** for a real Helm chart → neko-native → runs.

## Top risks & mitigations (of the chosen monolith shape)

1. **One crash domain.** A panic in any controller/CRI/CNI path can take down the
   whole agent. → Every `Reconcile` and the kubelet sync loop run under
   `recover()` (one bad key fails that key, not the process); the WAL makes full
   crash recovery cheap (replay on restart); the `store.Apply` path is the single
   funnel and gets near-100% test coverage + `-race` in CI.
2. **In-address-space concurrency.** Goroutines sharing `*Store` risk lost updates
   / races that a process boundary would have caught. → The store **only** hands
   out deep copies (never internal pointers) and the **only** mutator is `Apply`
   with a mandatory revision CAS — optimistic concurrency is not optional
   internally; controllers use the exact CAS path `nekoctl` does. `-race` from day one.
3. **Calcifying against multi-node.** The in-proc premise could make a second node
   a rewrite. → Mitigated by `access.StoreAccess`: the **Reader/Writer** halves are
   unchanged across local/remote; the **Watcher** adapter owns reconnect/resync so
   level-triggered controllers stay unchanged (interrogation M2 — *not* "nothing
   changes", the resync logic is real and lives in the adapter). We ship a
   loopback-HTTP Watcher in M1 to validate the seam early rather than at multi-node.
```

## Risk register (from interrogation — tracked, not yet resolved)

| ID | Risk | Status / plan |
|----|------|---------------|
| B1 | `sock_ops` can't load-balance | **Resolved in design:** LB is a `BPF_CGROUP_INET4_CONNECT` connect-hook program (`bpf/svc_lb`), separate C/Rust object. |
| B2 | neko-net ringbuf not pinned / single-consumer | **Resolved in design:** neko-box is sole loader; neko-net vendored as `.o`; its daemon doesn't run in the node. |
| M3 | eBPF viability in the Docker-VM kernel unverified | **✅ Verified** on this machine (kernel 6.10-linuxkit: BTF ✅, cgroup v2 ✅, bpffs ✅, `cgroup_sock_addr` + `sock_ops` program types ✅, all CONFIG ✅). Platform floor + `neko up` preflight specced in `PLATFORM.md`. Cgroup attach point = node-container root cgroup (validate at load). |
| M4 | Nested containerd/CNI underspecified (M1 make-or-break) | **Resolved** (`PLATFORM.md`): reuse `kindest/node` for M1; pin containerd `SystemdCgroup=false` (VM is cgroupfs); pod CIDR `10.244/16` + svc `10.96/12` (verified free here); **CRI-gRPC**. |
| M5a | DNS unsolved (Services half-useless without it) | **Resolved** (`PLATFORM.md`): embed CoreDNS in-process + a custom store-backed `neko` plugin (stock `kubernetes` plugin unusable — needs an apiserver). ~15–30 MB in-process; behind a `dns.Resolver` seam. |
| M5b | Latency depends on neko-net's unshipped M1.5 payload | **Open external dep.** First cut uses today's `RttEvent`; richer rollups wait on neko-net. |
| M2 | HTTP watch adapter semantics vs in-proc | **Mitigation scheduled:** loopback-HTTP Watcher + forced-disconnect test in M1. |
| m6 | WAL fsync policy / un-fsync'd crash window | **Resolved** (`PLATFORM.md`): `fdatasync` before publish by default (store never behind real state); `--wal-sync=batch` opt-out; add watch resume/compaction conformance test now. |
| D7 | Snapshot scope | **Resolved:** declared-state only; live CRIU checkpoint out of scope. |

