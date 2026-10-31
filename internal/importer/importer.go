// Package importer implements `neko import`: a ONE-WAY bridge from existing
// Kubernetes artifacts into neko-native objects (see docs: D3). It runs
// `helm template` on charts or parses k8s YAML, converts the supported subset
// (Deployment/Service/Namespace/...) into api.Object values, and applies them
// via access. There is NO live kubectl/helm wire — a live k8s-API proxy is a
// documented future nice-to-have, not built.
//
// Skeleton (architect Phase D).
package importer

import (
	"context"

	"github.com/neko-box/neko/internal/access"
)

// Source is a thing to import: a k8s YAML file/stream or a Helm chart.
type Source struct {
	// Path to a .yaml/.yml manifest or a .tgz chart; chart values; etc.
	Path string
	// IsChart selects `helm template` vs plain YAML parsing.
	IsChart bool
}

// Report summarizes what an import did.
type Report struct {
	Applied []string // kind/ns/name of applied objects
	Skipped []string // unsupported kinds, with reasons
}

// Import converts src and applies the supported objects through a.
func Import(ctx context.Context, a access.StoreAccess, src Source) (Report, error) {
	panic("not implemented")
}
