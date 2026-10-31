// Package store is neko-box's single source of truth: an in-memory object store
// with a monotonic revision, optimistic concurrency, in-process pub/sub watch,
// and a write-ahead log for durability. Concrete struct, one implementation —
// it is NOT an interface (per the "no interface without >=2 impls" rule).
//
// Only this package, internal/access, and internal/httpapi may touch the store
// internals; everything else goes through internal/access.StoreAccess.
package store

import (
	"context"
	"errors"
	"sync"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
)

// Key identifies an object within the store.
type Key struct {
	Kind      string
	Namespace string
	Name      string
}

// keyOf derives the store Key from an object.
func keyOf(obj api.Object) Key {
	m := obj.GetMeta()
	return Key{Kind: obj.GetType().Kind, Namespace: m.Namespace, Name: m.Name}
}

// EventType is the kind of change in a watch Event.
type EventType int

const (
	Added EventType = iota
	Modified
	Deleted
)

// Event is one change published to watchers, ordered by Revision.
type Event struct {
	Type     EventType
	Object   api.Object // post-image; the tombstone for Deleted
	Revision uint64
}

// Errors surfaced by the store.
var (
	// ErrConflict means the optimistic-concurrency revision check failed.
	ErrConflict = errors.New("store: revision conflict")
	// ErrTooOld means a Watch sinceRevision has been compacted past; the caller
	// must relist (level-triggered recovery).
	ErrTooOld = errors.New("store: watch revision compacted")
	// ErrNotFound is returned for a missing key where absence is an error.
	ErrNotFound = errors.New("store: not found")
	// ErrExists means a create (expectedRevision==0) hit an existing object.
	ErrExists = errors.New("store: already exists")
)

// watchBuffer is how many pending events a watcher may lag before it is dropped
// (its channel closed) and must relist. Bounds per-watcher memory.
const watchBuffer = 256

// watcher is one live Watch subscription.
type watcher struct {
	kind string // "" means all kinds
	ns   string // "" means all namespaces
	ch   chan Event
}

// Store holds live objects plus the revision counter, watch hub, and WAL.
// All exported methods are safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	rev      uint64
	objs     map[Key]api.Object
	watchers map[*watcher]struct{}
	wal      *wal // nil => in-memory only (no durability)
}

// New constructs a store backed by the WAL at walPath, replaying it on open.
// walPath == "" creates an in-memory-only store (no durability) — used in tests.
func New(walPath string) (*Store, error) {
	s := &Store{
		objs:     make(map[Key]api.Object),
		watchers: make(map[*watcher]struct{}),
	}
	if walPath == "" {
		return s, nil
	}
	w, err := openWAL(walPath, SyncEach)
	if err != nil {
		return nil, err
	}
	// Replay rebuilds state without re-logging or publishing.
	if err := w.replay(func(ev Event) error {
		s.objs[keyOf(ev.Object)] = ev.Object
		if ev.Type == Deleted {
			delete(s.objs, keyOf(ev.Object))
		}
		if ev.Revision > s.rev {
			s.rev = ev.Revision
		}
		return nil
	}); err != nil {
		w.close()
		return nil, err
	}
	s.wal = w
	return s, nil
}

// Get returns a deep copy of the object at k and whether it exists.
func (s *Store) Get(k Key) (api.Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	obj, ok := s.objs[k]
	if !ok {
		return nil, false
	}
	return obj.DeepCopyObject(), true
}

// List returns deep copies of all objects of kind in namespace matching sel,
// plus the store revision the snapshot was taken at. namespace=="" lists all.
func (s *Store) List(kind, namespace string, sel labels.Selector) ([]api.Object, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []api.Object
	for k, obj := range s.objs {
		if k.Kind != kind {
			continue
		}
		if namespace != "" && k.Namespace != namespace {
			continue
		}
		if !sel.Matches(obj.GetMeta().Labels) {
			continue
		}
		out = append(out, obj.DeepCopyObject())
	}
	return out, s.rev
}

// Apply is the single mutating entry point. expectedRevision==0 creates
// (ErrExists if the object exists); otherwise it is a compare-and-swap against
// the stored revision, returning ErrConflict on mismatch. On success it bumps
// the monotonic revision, stamps the object's Revision, appends to the WAL, then
// publishes the Event. Returns a deep copy of the stored, revision-stamped object.
func (s *Store) Apply(obj api.Object, expectedRevision uint64) (api.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := keyOf(obj)
	cur, exists := s.objs[k]

	switch {
	case expectedRevision == 0:
		if exists {
			return nil, ErrExists
		}
	default:
		if !exists {
			return nil, ErrNotFound
		}
		if cur.GetMeta().Revision != expectedRevision {
			return nil, ErrConflict
		}
	}

	// Store an independent copy; stamp the new revision on it.
	stored := obj.DeepCopyObject()
	s.rev++
	stored.GetMeta().Revision = s.rev
	if !exists {
		stored.GetMeta().CreationRev = s.rev
	} else {
		stored.GetMeta().CreationRev = cur.GetMeta().CreationRev
	}

	evType := Added
	if exists {
		evType = Modified
	}
	ev := Event{Type: evType, Object: stored, Revision: s.rev}

	// Write-ahead: durability before the event is visible to watchers.
	if s.wal != nil {
		if err := s.wal.append(ev); err != nil {
			s.rev-- // roll back the counter; nothing was published
			return nil, err
		}
	}
	s.objs[k] = stored
	s.publish(ev)

	return stored.DeepCopyObject(), nil
}

// Delete removes the object at k under the same optimistic-concurrency rule.
func (s *Store) Delete(k Key, expectedRevision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, exists := s.objs[k]
	if !exists {
		return ErrNotFound
	}
	if expectedRevision != 0 && cur.GetMeta().Revision != expectedRevision {
		return ErrConflict
	}

	s.rev++
	tombstone := cur.DeepCopyObject()
	tombstone.GetMeta().Revision = s.rev
	tombstone.GetMeta().Deleting = true
	ev := Event{Type: Deleted, Object: tombstone, Revision: s.rev}

	if s.wal != nil {
		if err := s.wal.append(ev); err != nil {
			s.rev--
			return err
		}
	}
	delete(s.objs, k)
	s.publish(ev)
	return nil
}

// publish fans ev out to matching watchers. Must hold s.mu. A watcher whose
// buffer is full is dropped (channel closed) — it must relist.
func (s *Store) publish(ev Event) {
	k := keyOf(ev.Object)
	for w := range s.watchers {
		if w.kind != "" && w.kind != k.Kind {
			continue
		}
		if w.ns != "" && w.ns != k.Namespace {
			continue
		}
		select {
		case w.ch <- ev:
		default:
			// Slow consumer: drop it. Closing signals "relist".
			close(w.ch)
			delete(s.watchers, w)
		}
	}
}

// Watch streams events for kind/namespace strictly ordered by revision, starting
// after sinceRevision. sinceRevision==0 replays current state as Added events
// then tails live. The channel closes on ctx cancellation or if the consumer
// lags past watchBuffer (caller must then relist).
//
// NOTE (M1): there is no compaction yet, so ErrTooOld is not returned — a
// sinceRevision in the past is served by replaying current state. Compaction and
// ErrTooOld arrive with the HTTP watch adapter (see docs risk M2/m6).
func (s *Store) Watch(ctx context.Context, kind, namespace string, sinceRevision uint64) (<-chan Event, error) {
	s.mu.Lock()
	w := &watcher{kind: kind, ns: namespace, ch: make(chan Event, watchBuffer)}

	// Seed replay for sinceRevision==0: current matching state as Added events,
	// buffered before any live event can be published (we hold the lock).
	if sinceRevision == 0 {
		for k, obj := range s.objs {
			if kind != "" && k.Kind != kind {
				continue
			}
			if namespace != "" && k.Namespace != namespace {
				continue
			}
			w.ch <- Event{Type: Added, Object: obj.DeepCopyObject(), Revision: obj.GetMeta().Revision}
		}
	}
	s.watchers[w] = struct{}{}
	s.mu.Unlock()

	// Tear down on context cancellation.
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if _, ok := s.watchers[w]; ok {
			delete(s.watchers, w)
			close(w.ch)
		}
		s.mu.Unlock()
	}()

	return w.ch, nil
}

// CurrentRevision returns the latest store revision.
func (s *Store) CurrentRevision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// Snapshot writes a consistent, declared-state snapshot (objects + revision) to
// an opaque blob. It does NOT capture running containers (see docs: D7).
func (s *Store) Snapshot() ([]byte, error) {
	panic("not implemented")
}

// Restore replaces store contents from a Snapshot blob. The kubelet then
// re-converges containers from the restored specs.
func (s *Store) Restore(blob []byte) error {
	panic("not implemented")
}
