// Package httpapi is the ONE external seam: a lean REST + SSE-watch surface that
// nekoctl (and, in multi-node, remote node agents) speak. It is the only place
// objects are serialized. It routes HTTP verbs to the store via access, streams
// watches as NDJSON, and exposes latency, snapshot/restore, and healthz.
//
// Wire contract (neko-native, /v1; see docs/ARCHITECTURE.md). Optimistic
// concurrency rides on the ?rev= query param -> Apply; 409 on ErrConflict.
//
// Skeleton (architect Phase D).
package httpapi

import (
	"net/http"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/observ"
	"github.com/neko-box/neko/internal/store"
)

// Server wraps the store (directly — it is one of the three store-touching
// packages) plus the latency rollup and snapshot entry points.
type Server struct {
	Store  *store.Store
	Access access.StoreAccess
	Observ *observ.Rollup
}

// New builds the HTTP server over the store.
func New(s *store.Store, ro *observ.Rollup) *Server {
	panic("not implemented")
}

// Routes returns the HTTP handler for the full /v1 surface.
func (s *Server) Routes() http.Handler {
	panic("not implemented")
}
