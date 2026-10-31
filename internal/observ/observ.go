// Package observ rolls up RTT samples from nekonet into bounded per-pod and
// per-service latency stats, joined to objects via the IP ownership IPAM and the
// store know. Served read-only through httpapi. A standout feature (see docs).
//
// Skeleton (architect Phase D).
package observ

import "github.com/neko-box/neko/internal/nekonet"

// LatencyStat is a bounded latency summary for one key (pod or service).
type LatencyStat struct {
	P50Micros uint32
	P95Micros uint32
	Samples   uint64
}

// Rollup aggregates samples keyed by pod/service.
type Rollup struct {
	// ring of per-key percentile sketches — elided in the skeleton.
}

// New creates an empty rollup.
func New() *Rollup {
	panic("not implemented")
}

// Ingest folds one sample into the rollups.
func (ro *Rollup) Ingest(s nekonet.Sample) {
	panic("not implemented")
}

// ForService returns the latency summary for a service, if any.
func (ro *Rollup) ForService(ns, name string) (LatencyStat, bool) {
	panic("not implemented")
}
