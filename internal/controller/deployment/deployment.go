// Package deployment reconciles Deployments into ReplicaSets. (M2 scope;
// scaffolded now so the controller.Controller interface has real implementations.)
package deployment

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns Deployment -> ReplicaSet.
type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string     { return "deployment" }
func (r *Reconciler) Kinds() []string  { return []string{"Deployment", "ReplicaSet"} }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	panic("not implemented")
}
