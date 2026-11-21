# rtt/ — neko-box RTT observer (C kprobe)

A neko-box-owned eBPF program that measures per-connection TCP round-trip time.

- `rtt.bpf.c` — a `kprobe/tcp_rcv_established` program. On each established-
  connection data segment it reads `tcp_sock.srtt_us >> 3` (microseconds) and the
  IPv4 4-tuple off the socket via CO-RE, and emits an `rtt_event` to the
  `RTT_EVENTS` ring buffer. GPL-licensed (required: kprobe + `BPF_CORE_READ`).
- `rtt.h` — the 24-byte `rtt_event` wire record shared with the Go decoder
  (`internal/nekonet`). Byte order is documented there (addresses network-order,
  ports host-order).

`internal/nekonet` is the sole loader and ring-buffer consumer; it attaches the
program via `link.Kprobe` and drains `RTT_EVENTS`.

## Why a kprobe, not sock_ops

This was originally the vendored `sock_ops` RTT program from the sibling
`neko-net` project, which emits on `BPF_SOCK_OPS_RTT_CB`. That callback **never
fires on the node kernel** (linuxkit, Docker Desktop) — verified: the program
loads, attaches, and runs (run_cnt climbs), but the ring buffer stays empty
(`bpftool map dump` = 0 elements). A kprobe on `tcp_rcv_established` fires
reliably on the same traffic. This is the mechanism real TCP-latency eBPF tools
(bcc `tcprtt`, Cilium) use, precisely because `sock_ops` RTT_CB is unreliable
across kernels. neko-net stays a reference; it is no longer built or loaded.

## Build

Compiled by the node image's `bpf` stage (`images/node/Dockerfile`):
`clang -target bpf -g -O2 -I ../include -I . -c rtt.bpf.c -o rtt.o`, baked to
`/opt/neko/bpf/rtt.o`. CO-RE relocates struct offsets against the node kernel's
BTF at load, so the committed `../include/vmlinux.h` need not match the node
kernel exactly.
