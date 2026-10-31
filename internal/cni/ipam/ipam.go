// Package ipam is a host-local CIDR allocator for pod IPs. State is rebuildable
// from the live Pods in the store on kubelet start, so it needs no separate
// persistence. Pod CIDR 10.244.0.0/16 (verified free on this host; see
// docs/PLATFORM.md M4).
//
// Skeleton (architect Phase D).
package ipam

import "net/netip"

// Allocator hands out and reclaims pod IPs from a CIDR.
type Allocator struct {
	// prefix + in-use set — elided in the skeleton.
}

// New creates an allocator over cidr (e.g. "10.244.0.0/16").
func New(cidr string) (*Allocator, error) {
	panic("not implemented")
}

// Allocate reserves and returns an IP for podUID (idempotent per UID).
func (a *Allocator) Allocate(podUID string) (netip.Addr, error) {
	panic("not implemented")
}

// Release returns podUID's IP to the pool.
func (a *Allocator) Release(podUID string) error {
	panic("not implemented")
}

// Rebuild restores allocation state from the IPs of currently-live pods.
func (a *Allocator) Rebuild(inUse map[string]netip.Addr) {
	panic("not implemented")
}
