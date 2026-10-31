// Package dns provides Service DNS. Decision D8: embed CoreDNS as a library with
// a custom store-backed "neko" plugin whose ServeDNS answers from the in-process
// store (the stock kubernetes plugin is unusable — it needs a client-go
// apiserver we do not have). Behind the Resolver seam so a lighter hand-rolled
// resolver stays a possible fallback.
//
// The CoreDNS dependency (github.com/coredns/coredns + its plugin SDK:
// plugin.Handler's ServeDNS, plugin.Register, a pruned plugin.cfg) is added to
// go.mod when this is implemented — kept out of the skeleton to stay light.
//
// Skeleton (architect Phase D).
package dns

import (
	"context"

	"github.com/neko-box/neko/internal/access"
)

// Resolver is the node agent's DNS seam. The CoreDNS-backed implementation
// satisfies it; so could a minimal hand-rolled one.
type Resolver interface {
	// Serve runs the DNS server on addr until ctx is cancelled.
	Serve(ctx context.Context, addr string) error
}

// CoreDNS embeds a trimmed CoreDNS (the "neko" plugin + cache + errors) driven
// by a generated Corefile, reading records from the store via access.Reader +
// access.Watcher so entries stay live.
type CoreDNS struct {
	Reader access.Reader
	Watch  access.Watcher
}

var _ Resolver = (*CoreDNS)(nil)

// Serve starts the embedded CoreDNS server.
func (c *CoreDNS) Serve(ctx context.Context, addr string) error {
	panic("not implemented")
}
