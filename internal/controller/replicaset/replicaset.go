// Package replicaset reconciles ReplicaSets into Pods, calling placement to bind
// each new Pod to a node. (M2 scope.)
package replicaset

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/placement"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns ReplicaSet -> Pod count.
type Reconciler struct {
	Client access.StoreAccess
	Place  func(pod *api.Pod, nodes []placement.NodeView) (string, error)
}

func (r *Reconciler) Name() string    { return "replicaset" }
func (r *Reconciler) Kinds() []string { return []string{"ReplicaSet", "Pod"} }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	panic("not implemented")
}
