package controller

import (
	"context"
	"sync"
	"time"

	"github.com/neko-box/neko/internal/store"
)

// queue is a deduping work queue of store.Keys with delayed requeue. A key
// present in the queue once collapses repeated adds (level-triggered: we only
// need to know a key is dirty, not how many times). Safe for many producers and
// one consumer.
type queue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	dirty   map[store.Key]struct{} // queued, not yet handed to a worker
	queued  []store.Key            // FIFO order of dirty keys
	closed  bool
}

func newQueue() *queue {
	q := &queue{dirty: make(map[store.Key]struct{})}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// add marks a key dirty (no-op if already queued).
func (q *queue) add(k store.Key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.dirty[k]; ok {
		return
	}
	q.dirty[k] = struct{}{}
	q.queued = append(q.queued, k)
	q.cond.Signal()
}

// get blocks until a key is available or ctx is cancelled. The returned key is
// removed from the dirty set; call done when finished. ok=false means shutdown.
func (q *queue) get(ctx context.Context) (store.Key, bool) {
	// Wake the waiter on ctx cancellation.
	stop := context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.closed = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer stop()

	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.queued) == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return store.Key{}, false
	}
	k := q.queued[0]
	q.queued = q.queued[1:]
	delete(q.dirty, k)
	return k, true
}

// done acknowledges a processed key. (No in-flight tracking is needed for the
// level-triggered model; present for call-site symmetry and future dedup.)
func (q *queue) done(store.Key) {}

// requeueAfter re-adds a key after d (for retry backoff).
func (q *queue) requeueAfter(k store.Key, d time.Duration) {
	time.AfterFunc(d, func() { q.add(k) })
}
