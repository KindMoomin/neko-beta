// Package cri wraps containerd via the CRI gRPC API (k8s.io/cri-api) — NOT
// ttrpc (see docs: interrogation M4). It pulls images and manages pod sandboxes
// and containers for the kubelet.
//
// Concrete struct, not an interface (one impl; a fake lives next to a test if
// one is ever needed). Skeleton (architect Phase D).
package cri

import (
	"context"

	"github.com/neko-box/neko/internal/api"
)

// Runtime is a CRI-gRPC client to the in-node containerd.
type Runtime struct {
	// gRPC conn to the containerd CRI socket — elided in the skeleton.
}

// New dials the containerd CRI endpoint (unix socket).
func New(endpoint string) (*Runtime, error) {
	panic("not implemented")
}

// EnsureImage pulls ref if it is not already present.
func (rt *Runtime) EnsureImage(ctx context.Context, ref string) error {
	panic("not implemented")
}

// CreateSandbox creates the pod sandbox (and its netns) and returns its ID.
func (rt *Runtime) CreateSandbox(ctx context.Context, pod *api.Pod, netnsPath string) (sandboxID string, err error) {
	panic("not implemented")
}

// StartContainer creates and starts one container in the sandbox.
func (rt *Runtime) StartContainer(ctx context.Context, sandboxID string, c api.Container) (id string, err error) {
	panic("not implemented")
}

// Stop stops and removes the sandbox and its containers.
func (rt *Runtime) Stop(ctx context.Context, sandboxID string) error {
	panic("not implemented")
}
