// Package kubelet is the node agent's pod lifecycle loop. It watches Pods bound
// to this node (via access.Watcher), and for each drives cni.Setup -> cri image
// pull/sandbox/container, then writes status back through access.Writer. It is
// level-triggered: it syncs desired (store) against actual (cri) each wake.
//
// Skeleton (architect Phase D).
package kubelet

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/cni"
	"github.com/neko-box/neko/internal/cri"
)

// Kubelet runs pods bound to NodeName on this node.
type Kubelet struct {
	NodeName string
	Client   access.StoreAccess
	Net      *cni.Network
	Runtime  *cri.Runtime
}

// New constructs a kubelet for one node.
func New(nodeName string, client access.StoreAccess, net *cni.Network, rt *cri.Runtime) *Kubelet {
	panic("not implemented")
}

// Run watches bound pods and reconciles them to running containers until ctx done.
func (k *Kubelet) Run(ctx context.Context) error {
	panic("not implemented")
}
