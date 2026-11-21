# bpf/ — the eBPF programs neko-box owns

**Non-Go.** These compile to BPF object files that the Go side
(`internal/nekonet`) loads and attaches. They are here, not in `internal/`,
because Go cannot author eBPF programs (decision D1/D2).

## Programs

### `svc_lb/` — Service load-balancer (kube-proxy replacement)
A **`BPF_CGROUP_INET4_CONNECT`** cgroup/connect-hook program (+ `INET6_CONNECT`,
UDP `SENDMSG`/`RECVMSG`, and `GETSOCKNAME` fix-up). At `connect()` time it looks
up the `svc_lb` map (written from Go by `internal/dataplane`) and rewrites the
destination from a ClusterIP to a chosen backend — socket-level DNAT, exactly
how Cilium replaces kube-proxy.

> Why not `sock_ops`: `sock_ops` is a notification hook; by the time it fires the
> 4-tuple is chosen and it cannot rewrite the destination. The connect hook runs
> *inside* `connect()` and can. (interrogation B1.)

Verified loadable on this machine: `cgroup_sock_addr` program type is available
(see `docs/PLATFORM.md`).

### `rtt/` — per-connection TCP RTT observer
A **neko-box-owned C kprobe** (`rtt/rtt.bpf.c`) on the kernel's
`tcp_rcv_established`. It reads the smoothed RTT (`tcp_sock.srtt_us >> 3`) and the
connection 4-tuple off each socket via CO-RE and emits an `rtt_event` (see
`rtt/rtt.h`) to the `RTT_EVENTS` ring buffer, which `internal/nekonet` drains.

> History: this was originally the vendored `sock_ops` RTT program from the
> sibling `neko-net` project, but that program's `BPF_SOCK_OPS_RTT_CB` callback
> never fires on the node kernel (linuxkit) — verified: program loads, attaches,
> and runs, but the ring buffer stays empty. A kprobe on `tcp_rcv_established`
> (the mechanism bcc `tcprtt` / Cilium use) fires reliably. neko-net remains a
> reference; it is no longer built or loaded.

## Language & build

C (clang, CO-RE against a committed `include/vmlinux.h`, relocated to the node
kernel's BTF at load). The node image builds the object (see
`images/node/Dockerfile`, the `bpf` stage) to `/opt/neko/bpf/rtt.o`;
`internal/nekonet` loads it via `cilium/ebpf`. `svc_lb` (Phase 2) will share this
same C/clang toolchain and `vmlinux.h`.
