// Package dataplane writes the svc_lb BPF map that the connect-hook LB program
// (bpf/svc_lb, a BPF_CGROUP_INET4_CONNECT program — see docs: D6/B1) reads to
// rewrite ClusterIP connections to a backend. Go only writes the map; the LB
// decision happens in-kernel at connect() time. There is no kube-proxy, no
// iptables. Map writes will use cilium/ebpf once implemented.
//
// Skeleton (architect Phase D).
package dataplane

import "net/netip"

// Backend is one EndpointSlice-derived target for a ClusterIP.
type Backend struct {
	IP   netip.Addr
	Port uint16
}

// Programmer writes the svc_lb map, diff-based and idempotent.
type Programmer struct {
	// *ebpf.Map handle for svc_lb — elided in the skeleton.
}

// New opens (or loads+pins) the svc_lb map.
func New() (*Programmer, error) {
	panic("not implemented")
}

// SetService replaces the backend set for a ClusterIP:port.
func (p *Programmer) SetService(clusterIP netip.Addr, port uint16, proto uint8, backends []Backend) error {
	panic("not implemented")
}

// RemoveService clears a ClusterIP:port's backends.
func (p *Programmer) RemoveService(clusterIP netip.Addr, port uint16, proto uint8) error {
	panic("not implemented")
}
