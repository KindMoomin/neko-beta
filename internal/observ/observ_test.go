package observ

import "testing"

func TestHistogramQuantiles(t *testing.T) {
	var h histogram
	// 100 samples clustered: 90 fast (~1ms), 10 slow (~100ms).
	for range 90 {
		h.add(1_000)
	}
	for range 10 {
		h.add(100_000)
	}
	if h.total != 100 {
		t.Fatalf("total = %d, want 100", h.total)
	}
	// p50 lands in the fast cluster; p95 crosses into the slow tail.
	p50 := h.quantile(0.50)
	p95 := h.quantile(0.95)
	if p50 > 2_000 {
		t.Errorf("p50 = %d us, want <= ~1-2ms (fast cluster)", p50)
	}
	if p95 < 50_000 {
		t.Errorf("p95 = %d us, want >= ~50ms (slow tail)", p95)
	}
}

func TestHistogramEmpty(t *testing.T) {
	var h histogram
	if got := h.quantile(0.50); got != 0 {
		t.Errorf("empty quantile = %d, want 0", got)
	}
}

func TestHistogramOverflowBucket(t *testing.T) {
	var h histogram
	h.add(5_000_000) // 5s — above the last edge (500ms)
	if h.counts[numBuckets-1] != 1 {
		t.Errorf("overflow bucket count = %d, want 1", h.counts[numBuckets-1])
	}
	// Reports the last edge as the best upper bound we have.
	if got := h.quantile(0.50); got != bucketBoundsMicros[len(bucketBoundsMicros)-1] {
		t.Errorf("overflow quantile = %d, want %d", got, bucketBoundsMicros[len(bucketBoundsMicros)-1])
	}
}
