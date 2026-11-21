// Package nekonet is the single BPF loader/attacher for the node. It loads
// neko-box's RTT kprobe program (baked into the node image at
// /opt/neko/bpf/rtt.o from bpf/rtt/rtt.bpf.c), attaches it via a kprobe on the
// kernel's tcp_rcv_established, and drains the RTT ring buffer.
//
// History (see docs: D2): the RTT program was originally the vendored neko-net
// sock_ops program, but its BPF_SOCK_OPS_RTT_CB callback never fires on the
// node kernel (linuxkit). neko-box now owns a C kprobe that reads srtt_us off
// each socket — the mechanism real TCP-latency tools use. neko-net remains a
// reference, no longer built or loaded.
//
// The loader itself (Load/ReadRTT/Close) is Linux-only — it uses cilium/ebpf,
// whose link/ringbuf packages don't build on macOS. The implementation lives in
// nekonet_linux.go; this file holds the shared types and a non-linux stub so the
// rest of the module still builds and tests on the dev machine.
package nekonet

import "net/netip"

// bpfObjectPath is where the node image bakes the compiled RTT object
// (see images/node/Dockerfile). Program symbol `neko_rtt`, ringbuf `RTT_EVENTS`.
const bpfObjectPath = "/opt/neko/bpf/rtt.o"

// Sample is neko-box's RTT record. The on-wire layout is bpf/rtt/rtt.h's
// rtt_event, decoded cgo-free; observ enriches it against the store. Reading the
// full socket at tcp_rcv_established means SrcIP/SrcPort/StampNanos are now
// populated too (the old sock_ops payload was dst-only).
type Sample struct {
	SrcIP, DstIP     netip.Addr
	SrcPort, DstPort uint16
	RTTMicros        uint32
	StampNanos       uint64
}
