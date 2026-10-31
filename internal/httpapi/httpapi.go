// Package httpapi is the ONE external seam: a lean REST + SSE-watch surface that
// nekoctl (and, in multi-node, remote node agents) speak. It is the only place
// objects are serialized. It routes HTTP verbs to the store, streams watches as
// server-sent NDJSON, and exposes healthz (latency/snapshot arrive with their
// subsystems).
//
// Wire contract (neko-native, /v1; see docs/ARCHITECTURE.md). Optimistic
// concurrency rides on the ?rev= query param -> Apply; 409 on ErrConflict.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
	"github.com/neko-box/neko/internal/observ"
	"github.com/neko-box/neko/internal/store"
)

// Server wraps the store directly — httpapi is one of the three packages allowed
// to touch store internals. Observ may be nil until the latency subsystem exists.
type Server struct {
	store  *store.Store
	observ *observ.Rollup
}

// New builds the HTTP server over the store.
func New(s *store.Store, ro *observ.Rollup) *Server {
	return &Server{store: s, observ: ro}
}

// Routes returns the HTTP handler for the /v1 surface.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", s.healthz)
	mux.HandleFunc("GET /v1/{kind}", s.listOrWatch) // ?watch=1 switches to SSE
	mux.HandleFunc("GET /v1/{kind}/{ns}/{name}", s.get)
	mux.HandleFunc("POST /v1/{kind}/{ns}", s.create)
	mux.HandleFunc("PUT /v1/{kind}/{ns}/{name}", s.replace)
	mux.HandleFunc("DELETE /v1/{kind}/{ns}/{name}", s.delete)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	// ready:true is meaningful only after WAL replay; New returns a store whose
	// replay already completed, so if we're serving we're ready.
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "revision": s.store.CurrentRevision()})
}

// listResponse is the envelope for a collection.
type listResponse struct {
	Items    []api.Object `json:"items"`
	Revision uint64       `json:"revision"`
}

func (s *Server) listOrWatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "1" {
		s.watch(w, r)
		return
	}
	kind := r.PathValue("kind")
	ns := r.URL.Query().Get("namespace") // "" => all namespaces
	sel := parseSelector(r.URL.Query().Get("labelSelector"))
	items, rev := s.store.List(kind, ns, sel)
	if items == nil {
		items = []api.Object{}
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Revision: rev})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	k := store.Key{Kind: r.PathValue("kind"), Namespace: r.PathValue("ns"), Name: r.PathValue("name")}
	obj, ok := s.store.Get(k)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	obj, err := s.decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Path namespace is authoritative for create.
	obj.GetMeta().Namespace = r.PathValue("ns")
	stored, err := s.store.Apply(obj, 0)
	if err != nil {
		s.writeApplyErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (s *Server) replace(w http.ResponseWriter, r *http.Request) {
	rev, err := revParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rev")
		return
	}
	if rev == 0 {
		writeErr(w, http.StatusBadRequest, "PUT requires ?rev= (use POST to create)")
		return
	}
	obj, err := s.decodeBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m := obj.GetMeta()
	m.Namespace = r.PathValue("ns")
	m.Name = r.PathValue("name")
	stored, err := s.store.Apply(obj, rev)
	if err != nil {
		s.writeApplyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	rev, err := revParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rev")
		return
	}
	k := store.Key{Kind: r.PathValue("kind"), Namespace: r.PathValue("ns"), Name: r.PathValue("name")}
	if err := s.store.Delete(k, rev); err != nil {
		s.writeApplyErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// watch streams events as server-sent NDJSON (one JSON object per line), flushing
// each. sinceRevision=0 replays current state then tails. Ends on client cancel.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	since, err := sinceParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid since")
		return
	}
	kind := r.PathValue("kind")
	ns := r.URL.Query().Get("namespace")

	ch, err := s.store.Watch(r.Context(), kind, ns, since)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	for ev := range ch {
		if err := enc.Encode(wireEventOf(ev)); err != nil {
			return // client went away
		}
		flusher.Flush()
	}
}

// --- helpers ---------------------------------------------------------------

// wireEvent is the on-wire form of a watch event.
type wireEvent struct {
	Type     string     `json:"type"`
	Revision uint64     `json:"revision"`
	Object   api.Object `json:"object"`
}

func (s *Server) decodeBody(r *http.Request) (api.Object, error) {
	kind := r.PathValue("kind")
	obj, err := api.New(kind)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(obj); err != nil {
		return nil, err
	}
	// Kind in the path is authoritative over whatever the body claimed.
	tm := obj.GetType()
	if tm.Kind != "" && tm.Kind != kind {
		return nil, errors.New("body kind does not match path")
	}
	return obj, nil
}

func (s *Server) writeApplyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "revision conflict")
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func revParam(r *http.Request) (uint64, error) {
	v := r.URL.Query().Get("rev")
	if v == "" {
		return 0, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

func sinceParam(r *http.Request) (uint64, error) {
	v := r.URL.Query().Get("since")
	if v == "" {
		return 0, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

func parseSelector(s string) labels.Selector {
	if s == "" {
		return nil
	}
	sel := labels.Selector{}
	// Minimal: comma-separated key=value pairs (equality-only, matches labels pkg).
	for pair := range strings.SplitSeq(s, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			sel[k] = v
		}
	}
	return sel
}

var eventTypeName = map[store.EventType]string{
	store.Added:    "ADDED",
	store.Modified: "MODIFIED",
	store.Deleted:  "DELETED",
}

func wireEventOf(ev store.Event) wireEvent {
	return wireEvent{Type: eventTypeName[ev.Type], Revision: ev.Revision, Object: ev.Object}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
