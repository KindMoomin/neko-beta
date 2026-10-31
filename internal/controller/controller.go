// Package controller is the reconcile runtime: it wires each Controller to the
// store's watch (via access.Watcher), funnels events into per-key rate-limited
// workqueues, and wraps every Reconcile in recover() so one bad key never kills
// the process. Reconcilers are level-triggered: each reads full desired+actual
// through access and converges with optimistic-concurrency Apply, so a missed
// watch event self-heals on the next relist.
//
// Controller IS an interface — it has multiple implementations (see the
// controllers/ subpackages), so it earns its keep.
//
// Skeleton (architect Phase D).
package controller

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/store"
)

// Controller reconciles one or more kinds. The run loop calls Reconcile with the
// key of a changed object; returning an error requeues it with backoff.
type Controller interface {
	Name() string
	Kinds() []string
	Reconcile(ctx context.Context, key store.Key) error
}

// Run builds informers on a.Watch for every controller's kinds, fans events into
// per-key workqueues, and drives Reconcile under recover(). Blocks until ctx done.
func Run(ctx context.Context, a access.StoreAccess, controllers ...Controller) error {
	panic("not implemented")
}
