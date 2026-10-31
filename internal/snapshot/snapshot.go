// Package snapshot orchestrates declared-state cluster snapshot/restore (D7):
// the store's objects + revision (+ the svc_lb map contents). It does NOT
// capture running containers, connections, or netns — restore re-declares and
// the kubelet re-converges. CRIU-style live checkpoint is out of scope.
//
// Skeleton (architect Phase D).
package snapshot

import "github.com/neko-box/neko/internal/store"

// Manager snapshots and restores declared cluster state.
type Manager struct {
	Store *store.Store
}

// Save writes a declared-state snapshot blob.
func (m *Manager) Save() ([]byte, error) {
	panic("not implemented")
}

// Restore replaces store state from a snapshot blob; the kubelet re-converges.
func (m *Manager) Restore(blob []byte) error {
	panic("not implemented")
}
