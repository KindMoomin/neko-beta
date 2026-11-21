//go:build !linux

package nekonet

import (
	"context"
	"errors"
)

// errNotLinux is returned off the node: the dataplane only runs inside the
// Linux node container. This keeps the package building/testing on macOS.
var errNotLinux = errors.New("nekonet: the eBPF dataplane only runs on the node (linux)")

// Dataplane owns the loaded BPF collection and the RTT ring reader. On non-linux
// it holds nothing; all methods report errNotLinux.
type Dataplane struct{}

// Load is a no-op stub off the node.
func (d *Dataplane) Load(ctx context.Context) error { return errNotLinux }

// ReadRTT is a no-op stub off the node.
func (d *Dataplane) ReadRTT(ctx context.Context, out chan<- Sample) error { return errNotLinux }

// Close is a no-op stub off the node.
func (d *Dataplane) Close() error { return nil }
