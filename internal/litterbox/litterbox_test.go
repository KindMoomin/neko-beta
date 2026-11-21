package litterbox

import (
	"testing"

	"github.com/neko-box/neko/internal/api"
)

// TestPodKeyDistinctForReplicas is the regression test for the "only 1 of N
// deployment pods starts" bug. The litterbox dedups per-pod work (the "busy"
// and "sandboxes" maps) by podKey. The bug was keying by pod.Meta.UID, which
// the replicaset controller never sets — so every replica shared the empty-UID
// key, and the dedup guard started only one sandbox.
//
// podKey must give DISTINCT keys to distinct replica pods even when their UID
// is empty. Keying by namespace/name (the store identity) guarantees that.
func TestPodKeyDistinctForReplicas(t *testing.T) {
	// Two replicas as internal/controller/replicaset.createPod makes them:
	// distinct names, empty UID.
	p0 := &api.Pod{Meta: api.ObjectMeta{Namespace: "default", Name: "web-rs-0"}}
	p1 := &api.Pod{Meta: api.ObjectMeta{Namespace: "default", Name: "web-rs-1"}}

	if podKey(p0) == podKey(p1) {
		t.Fatalf("podKey collided for distinct replicas with empty UID: %q == %q",
			podKey(p0), podKey(p1))
	}
}

// TestPodKeySameForSamePod ensures repeated events for one pod coalesce (the
// dedup must still work): same identity -> same key.
func TestPodKeySameForSamePod(t *testing.T) {
	a := &api.Pod{Meta: api.ObjectMeta{Namespace: "default", Name: "web-rs-0", UID: "u1"}}
	b := &api.Pod{Meta: api.ObjectMeta{Namespace: "default", Name: "web-rs-0", UID: "u1"}}
	if podKey(a) != podKey(b) {
		t.Fatalf("podKey differed for the same pod: %q != %q", podKey(a), podKey(b))
	}
}
