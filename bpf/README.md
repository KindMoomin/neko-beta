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
The **vendored** `sock_ops` program from the sibling `neko-net` project, built
to a `.o`. neko-box is the **sole loader and ring-buffer consumer** — neko-net's
own userspace daemon does NOT run in the node container (interrogation B2).

## Language & build (to be decided at implementation)

Either C (clang/libbpf, CO-RE against the VM kernel's BTF) or Rust (aya, as
neko-net already uses). The RTT program is reused from neko-net, so matching its
Rust/aya toolchain for both is the lower-friction path. The build emits objects
that `internal/nekonet` loads via `cilium/ebpf`.

Build wiring is intentionally not implemented in this skeleton.
