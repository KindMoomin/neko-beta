// Package service reconciles Services + ready Pods into EndpointSlices, and
// programs the svc_lb eBPF map (two sinks, one reconcile — see docs). (M2 scope.)
package service

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/dataplane"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns Service/EndpointSlice and the svc_lb map backends.
type Reconciler struct {
	Client access.StoreAccess
	LB     *dataplane.Programmer
}

func (r *Reconciler) Name() string    { return "service" }
func (r *Reconciler) Kinds() []string { return []string{"Service", "Pod", "EndpointSlice"} }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	panic("not implemented")
}
