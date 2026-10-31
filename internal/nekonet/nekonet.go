// Package nekonet is the single BPF loader/attacher for the node. It loads the
// two eBPF objects neko-box owns — the vendored neko-net sock_ops RTT program
// (bpf/rtt) and the svc_lb connect-hook LB program (bpf/svc_lb) — attaches them
// to the node-container cgroup, and drains the RTT ring buffer.
//
// Ownership note (see docs: B2): neko-box is the SOLE loader and consumer.
// neko-net ships only as a compiled .o; its userspace daemon does not run here.
//
// Skeleton (architect Phase D).
package nekonet

import (
	"context"
	"net/netip"
)

// Sample is neko-box's enriched RTT record. The on-wire ring buffer layout is
// neko-net's RttEvent (daddr/dport/srtt_us today — src/port/timestamp depend on
// neko-net's unshipped M1.5, see docs: M5b); nekonet decodes it cgo-free and
// enriches what it can from the store.
type Sample struct {
	SrcIP, DstIP     netip.Addr
	SrcPort, DstPort uint16
	RTTMicros        uint32
	StampNanos       uint64
}

// Dataplane owns the loaded BPF collections and the RTT ring reader.
type Dataplane struct {
	// loaded collections + ring reader — elided in the skeleton.
}

// Load loads and attaches both BPF objects to cgroupPath.
func (d *Dataplane) Load(ctx context.Context, cgroupPath string) error {
	panic("not implemented")
}

// ReadRTT drains the RTT ring buffer into out until ctx is cancelled.
func (d *Dataplane) ReadRTT(ctx context.Context, out chan<- Sample) error {
	panic("not implemented")
}

// Close detaches programs and releases map handles.
func (d *Dataplane) Close() error {
	panic("not implemented")
}
