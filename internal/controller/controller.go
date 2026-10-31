// Package controller is the reconcile runtime: it wires each Controller to the
// store's watch (via access.Watcher), funnels change keys into a per-controller
// rate-limited workqueue, and wraps every Reconcile in recover() so one bad key
// never kills the process. Reconcilers are level-triggered: each reads full
// desired+actual through access and converges with optimistic-concurrency Apply,
// so a missed or duplicated watch event self-heals on the next pass.
//
// Controller IS an interface — it has multiple implementations (the controllers/
// subpackages), so it earns its keep.
package controller

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/store"
)

// Controller reconciles one or more kinds. The run loop calls Reconcile with the
// key of a changed object; returning an error requeues it with backoff.
//
// MapToReconcileKey lets a controller translate a change event (including the
// object, so tombstones on delete are usable) into the key it wants to
// reconcile — e.g. a Pod event maps to its owning ReplicaSet. Returning ok=false
// drops the event. This is the "enqueue request for owner" pattern; it keeps
// Reconcile itself purely key-based and level-triggered.
type Controller interface {
	Name() string
	Kinds() []string
	MapToReconcileKey(ev store.Event) (store.Key, bool)
	Reconcile(ctx context.Context, key store.Key) error
}

// retryDelay is the fixed backoff before a failed key is retried. A single delay
// (not exponential) is enough for a local, low-churn cluster; keep it simple.
const retryDelay = 500 * time.Millisecond

// Run starts every controller: one watch per (controller, kind), a shared
// workqueue per controller, and a worker draining it. Blocks until ctx is done.
func Run(ctx context.Context, a access.StoreAccess, controllers ...Controller) error {
	var wg sync.WaitGroup
	for _, c := range controllers {
		q := newQueue()
		// One watch per kind this controller cares about; every event is mapped
		// (via the controller) to the key it wants reconciled, then enqueued.
		for _, kind := range c.Kinds() {
			wg.Add(1)
			go func(c Controller, kind string) {
				defer wg.Done()
				watchInto(ctx, a, c, kind, q)
			}(c, kind)
		}
		wg.Add(1)
		go func(c Controller, q *queue) {
			defer wg.Done()
			work(ctx, c, q)
		}(c, q)
	}
	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

// watchInto subscribes to one kind and enqueues the reconcile key each event
// maps to (per the controller), re-subscribing if the watch drops.
func watchInto(ctx context.Context, a access.StoreAccess, c Controller, kind string, q *queue) {
	for {
		ch, err := a.Watch(ctx, kind, "", 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(retryDelay)
			continue
		}
		for ev := range ch {
			if key, ok := c.MapToReconcileKey(ev); ok {
				q.add(key)
			}
		}
		if ctx.Err() != nil {
			return
		}
		// channel closed without ctx cancel => relist by re-watching.
	}
}

// work drains the queue, calling Reconcile under recover(); failures requeue
// after retryDelay.
func work(ctx context.Context, c Controller, q *queue) {
	for {
		key, ok := q.get(ctx)
		if !ok {
			return
		}
		err := safeReconcile(ctx, c, key)
		if err != nil {
			log.Printf("controller %s: reconcile %v: %v (requeue)", c.Name(), key, err)
			q.requeueAfter(key, retryDelay)
		}
		q.done(key)
	}
}

// safeReconcile runs Reconcile and converts a panic into an error so one bad key
// never takes down the process.
func safeReconcile(ctx context.Context, c Controller, key store.Key) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{what: r}
		}
	}()
	return c.Reconcile(ctx, key)
}

type panicError struct{ what any }

func (e *panicError) Error() string { return "panic in reconcile: " + sprint(e.what) }

func sprint(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "non-string panic"
}
