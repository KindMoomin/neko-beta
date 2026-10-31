// Package namespace reconciles namespace lifecycle, including cascading delete of
// objects in a namespace marked Deleting. (M2 scope.)
package namespace

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns Namespace lifecycle.
type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string    { return "namespace" }
func (r *Reconciler) Kinds() []string { return []string{"Namespace"} }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	panic("not implemented")
}
