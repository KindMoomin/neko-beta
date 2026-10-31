// Package access is the narrow seam through which controllers, the kubelet, and
// nekoctl reach cluster state — so they are identical whether state is local
// (in-process) or remote (HTTP, multi-node). This is the ONE interface we build
// ahead of its second implementation: InProc exists now; an HTTP adapter arrives
// with multi-node. The Reader/Writer halves translate to HTTP cleanly; the
// Watcher half is where the HTTP adapter will own reconnect/resync so that
// level-triggered callers stay unchanged (see docs: interrogation M2).
//
// Skeleton (architect Phase D).
package access

import (
	"context"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
	"github.com/neko-box/neko/internal/store"
)

// Reader reads cluster state.
type Reader interface {
	Get(ctx context.Context, k store.Key) (api.Object, bool, error)
	List(ctx context.Context, kind, ns string, sel labels.Selector) ([]api.Object, uint64, error)
}

// Watcher streams changes. Contract (both impls must honour): events strictly
// ordered by revision; sinceRev==0 replays live state then tails; a compacted
// sinceRev yields store.ErrTooOld and the caller relists. The ADAPTER, not the
// caller, owns detecting disconnect/gap and triggering resync.
type Watcher interface {
	Watch(ctx context.Context, kind, ns string, sinceRev uint64) (<-chan store.Event, error)
}

// Writer mutates cluster state. The revision for optimistic concurrency rides
// on obj.GetMeta().Revision (0 => create).
type Writer interface {
	Apply(ctx context.Context, obj api.Object) (api.Object, error)
	Delete(ctx context.Context, k store.Key, rev uint64) error
}

// StoreAccess is the full seam.
type StoreAccess interface {
	Reader
	Watcher
	Writer
}

// InProc satisfies StoreAccess with zero serialization by calling the in-process
// store directly — the single-node fast path.
type InProc struct {
	S *store.Store
}

var _ StoreAccess = (*InProc)(nil)

func (a *InProc) Get(ctx context.Context, k store.Key) (api.Object, bool, error) {
	obj, ok := a.S.Get(k)
	return obj, ok, nil
}

func (a *InProc) List(ctx context.Context, kind, ns string, sel labels.Selector) ([]api.Object, uint64, error) {
	objs, rev := a.S.List(kind, ns, sel)
	return objs, rev, nil
}

func (a *InProc) Watch(ctx context.Context, kind, ns string, sinceRev uint64) (<-chan store.Event, error) {
	return a.S.Watch(ctx, kind, ns, sinceRev)
}

func (a *InProc) Apply(ctx context.Context, obj api.Object) (api.Object, error) {
	return a.S.Apply(obj, obj.GetMeta().Revision)
}

func (a *InProc) Delete(ctx context.Context, k store.Key, rev uint64) error {
	return a.S.Delete(k, rev)
}

// httpClient (future, multi-node) will satisfy StoreAccess over the httpapi
// contract, owning reconnect/resync in its Watch. Declared here as the promised
// second implementation that justifies the interface; not built in M1.
