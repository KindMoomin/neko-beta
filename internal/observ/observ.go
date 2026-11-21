// Package observ rolls up RTT samples from nekonet into bounded per-destination
// latency stats, joined to Services via the object store. Served read-only
// through httpapi. A standout feature (see docs): zero-setup per-service latency.
//
// Join model (Phase 1): a Sample carries a destination IP. We bucket samples by
// DstIP, and on query resolve a Service -> its selector -> matching Pods' PodIPs
// -> sum those IPs' histograms. This matches pod/endpoint IPs, NOT ClusterIPs
// (the svc_lb connect-hook / ClusterIP DNAT is Phase 2). The join reads the
// store through access.StoreAccess (not store internals).
package observ

import (
	"context"
	"sort"
	"sync"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
	"github.com/neko-box/neko/internal/nekonet"
	"github.com/neko-box/neko/internal/store"
)

// bucketBoundsMicros are the upper edges (inclusive) of the latency histogram
// buckets, in microseconds. A final implicit bucket catches everything above
// the last edge. Chosen log-ish across the dev-relevant range (0.5ms..500ms).
var bucketBoundsMicros = []uint32{
	500, 1_000, 2_000, 5_000, 10_000, 20_000, 50_000, 100_000, 200_000, 500_000,
}

// numBuckets includes the overflow bucket (> last edge).
const numBuckets = 11 // len(bucketBoundsMicros) + 1

// Compile-time guard so numBuckets stays in sync with bucketBoundsMicros.
var _ = [1]struct{}{}[numBuckets-(len(bucketBoundsMicros)+1)]

// histogram is a fixed-bucket counter for one destination IP. Bounded memory,
// no retained samples.
type histogram struct {
	counts [numBuckets]uint64
	total  uint64
}

func (h *histogram) add(rttMicros uint32) {
	i := sort.Search(len(bucketBoundsMicros), func(i int) bool {
		return rttMicros <= bucketBoundsMicros[i]
	})
	h.counts[i]++
	h.total++
}

// quantile returns an upper-bound estimate (the bucket's upper edge) for the
// given fraction q in [0,1]. The overflow bucket reports the last edge (we have
// no upper bound for it — a documented approximation for a dev-box readout).
func (h *histogram) quantile(q float64) uint32 {
	if h.total == 0 {
		return 0
	}
	// 1-based rank of the target sample; ceil so q>0 never resolves to rank 0
	// (which would spuriously match an empty leading bucket).
	rank := uint64(float64(h.total)*q + 0.999999)
	if rank == 0 {
		rank = 1
	}
	if rank > h.total {
		rank = h.total
	}
	var cum uint64
	for i := range numBuckets {
		cum += h.counts[i]
		if cum >= rank {
			if i < len(bucketBoundsMicros) {
				return bucketBoundsMicros[i]
			}
			return bucketBoundsMicros[len(bucketBoundsMicros)-1]
		}
	}
	return bucketBoundsMicros[len(bucketBoundsMicros)-1]
}

// LatencyStat is a bounded latency summary for one key (pod or service).
type LatencyStat struct {
	P50Micros uint32
	P95Micros uint32
	Samples   uint64
}

// Rollup aggregates samples by destination IP and joins to Services on query.
type Rollup struct {
	acc access.StoreAccess

	mu    sync.Mutex
	byIP  map[string]*histogram // destination IP string -> histogram
}

// New creates an empty rollup bound to the store for the Service join.
func New(acc access.StoreAccess) *Rollup {
	return &Rollup{acc: acc, byIP: make(map[string]*histogram)}
}

// Ingest folds one sample into the destination-IP histogram.
func (ro *Rollup) Ingest(s nekonet.Sample) {
	if !s.DstIP.IsValid() {
		return
	}
	ip := s.DstIP.String()
	ro.mu.Lock()
	h := ro.byIP[ip]
	if h == nil {
		h = &histogram{}
		ro.byIP[ip] = h
	}
	h.add(s.RTTMicros)
	ro.mu.Unlock()
}

// ForService returns the latency summary for a Service, aggregating the
// histograms of all its backend Pod IPs (resolved via the Service selector).
func (ro *Rollup) ForService(ns, name string) (LatencyStat, bool) {
	svcObj, ok, err := ro.acc.Get(context.Background(), store.Key{Kind: "Service", Namespace: ns, Name: name})
	if err != nil || !ok {
		return LatencyStat{}, false
	}
	svc, ok := svcObj.(*api.Service)
	if !ok || len(svc.Spec.Selector) == 0 {
		return LatencyStat{}, false
	}

	// Resolve backend Pod IPs: Pods in this namespace matching the selector.
	pods, _, err := ro.acc.List(context.Background(), "Pod", ns, labels.Selector(svc.Spec.Selector))
	if err != nil {
		return LatencyStat{}, false
	}

	agg := &histogram{}
	ro.mu.Lock()
	for _, o := range pods {
		p, ok := o.(*api.Pod)
		if !ok || p.Status.PodIP == "" {
			continue
		}
		if h := ro.byIP[p.Status.PodIP]; h != nil {
			for i := range agg.counts {
				agg.counts[i] += h.counts[i]
			}
			agg.total += h.total
		}
	}
	ro.mu.Unlock()

	if agg.total == 0 {
		return LatencyStat{}, false
	}
	return LatencyStat{
		P50Micros: agg.quantile(0.50),
		P95Micros: agg.quantile(0.95),
		Samples:   agg.total,
	}, true
}
