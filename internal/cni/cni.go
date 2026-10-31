// Package cni sets up pod networking: a network namespace, a veth pair into the
// node bridge, and an IP from IPAM. It also writes /etc/resolv.conf pointing at
// the cluster DNS IP (see docs: D8). Node-local; runs inside the kubelet.
//
// Skeleton (architect Phase D).
package cni

import (
	"context"
	"net"
	"net/netip"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/cni/ipam"
)

// PodNet describes a pod's configured network.
type PodNet struct {
	NetnsPath string
	IP        netip.Addr
	MAC       net.HardwareAddr
}

// Network configures pod networking over a bridge + IPAM.
type Network struct {
	ipam      *ipam.Allocator
	bridge    string
	clusterDNS netip.Addr
}

// New builds a Network bound to a bridge, IPAM allocator, and cluster DNS IP.
func New(bridge string, alloc *ipam.Allocator, clusterDNS netip.Addr) *Network {
	panic("not implemented")
}

// Setup creates the netns, allocates an IP, builds the veth into the bridge,
// and writes resolv.conf. Returns the pod's network details.
func (n *Network) Setup(ctx context.Context, pod *api.Pod) (PodNet, error) {
	panic("not implemented")
}

// Teardown releases the IP and removes the veth and netns.
func (n *Network) Teardown(ctx context.Context, pod *api.Pod) error {
	panic("not implemented")
}
