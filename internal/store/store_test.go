package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/labels"
)

func pod(ns, name string) *api.Pod {
	return &api.Pod{
		TypeMeta: api.TypeMeta{Kind: "Pod", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: name, Namespace: ns},
	}
}

func newMem(t *testing.T) *Store {
	t.Helper()
	s, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestApplyCreateStampsRevisionMonotonic(t *testing.T) {
	s := newMem(t)
	a, err := s.Apply(pod("default", "a"), 0)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if a.GetMeta().Revision != 1 {
		t.Errorf("first revision = %d, want 1", a.GetMeta().Revision)
	}
	b, err := s.Apply(pod("default", "b"), 0)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if b.GetMeta().Revision != 2 {
		t.Errorf("second revision = %d, want 2", b.GetMeta().Revision)
	}
	if s.CurrentRevision() != 2 {
		t.Errorf("CurrentRevision = %d, want 2", s.CurrentRevision())
	}
}

func TestApplyCreateTwiceIsExists(t *testing.T) {
	s := newMem(t)
	if _, err := s.Apply(pod("default", "a"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(pod("default", "a"), 0); err != ErrExists {
		t.Errorf("second create err = %v, want ErrExists", err)
	}
}

func TestApplyCASConflict(t *testing.T) {
	s := newMem(t)
	created, _ := s.Apply(pod("default", "a"), 0) // rev 1
	// Update with the right revision succeeds.
	created.(*api.Pod).Status.Phase = "Running"
	updated, err := s.Apply(created, created.GetMeta().Revision)
	if err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	if updated.GetMeta().Revision != 2 {
		t.Errorf("updated revision = %d, want 2", updated.GetMeta().Revision)
	}
	// Re-applying with the now-stale revision 1 must conflict.
	if _, err := s.Apply(created, 1); err != ErrConflict {
		t.Errorf("stale update err = %v, want ErrConflict", err)
	}
}

func TestGetReturnsIndependentCopy(t *testing.T) {
	s := newMem(t)
	p := pod("default", "a")
	p.Meta.Labels = map[string]string{"app": "x"}
	s.Apply(p, 0)

	got1, _ := s.Get(Key{"Pod", "default", "a"})
	got1.GetMeta().Labels["app"] = "mutated"

	got2, _ := s.Get(Key{"Pod", "default", "a"})
	if got2.GetMeta().Labels["app"] != "x" {
		t.Error("store leaked an aliased object: mutation via Get persisted")
	}
}

func TestListFiltersByKindNamespaceSelector(t *testing.T) {
	s := newMem(t)
	p1 := pod("default", "a")
	p1.Meta.Labels = map[string]string{"app": "web"}
	p2 := pod("default", "b")
	p2.Meta.Labels = map[string]string{"app": "db"}
	p3 := pod("other", "c")
	s.Apply(p1, 0)
	s.Apply(p2, 0)
	s.Apply(p3, 0)

	all, _ := s.List("Pod", "default", nil)
	if len(all) != 2 {
		t.Errorf("list default = %d pods, want 2", len(all))
	}
	web, _ := s.List("Pod", "default", labels.Selector{"app": "web"})
	if len(web) != 1 || web[0].GetMeta().Name != "a" {
		t.Errorf("selector list = %v, want [a]", names(web))
	}
	none, _ := s.List("Service", "", nil)
	if len(none) != 0 {
		t.Errorf("list of absent kind = %d, want 0", len(none))
	}
}

func TestDeleteConflictAndNotFound(t *testing.T) {
	s := newMem(t)
	if err := s.Delete(Key{"Pod", "default", "ghost"}, 0); err != ErrNotFound {
		t.Errorf("delete missing = %v, want ErrNotFound", err)
	}
	s.Apply(pod("default", "a"), 0) // rev 1
	if err := s.Delete(Key{"Pod", "default", "a"}, 99); err != ErrConflict {
		t.Errorf("delete wrong rev = %v, want ErrConflict", err)
	}
	if err := s.Delete(Key{"Pod", "default", "a"}, 1); err != nil {
		t.Errorf("delete right rev = %v, want nil", err)
	}
	if _, ok := s.Get(Key{"Pod", "default", "a"}); ok {
		t.Error("object still present after delete")
	}
}

func TestWatchReplayThenLiveOrdered(t *testing.T) {
	s := newMem(t)
	s.Apply(pod("default", "pre1"), 0) // rev 1
	s.Apply(pod("default", "pre2"), 0) // rev 2

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := s.Watch(ctx, "Pod", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Two replay events (order among them is unspecified; count + revisions aren't).
	got := map[string]bool{}
	for range 2 {
		ev := recvOrFail(t, ch)
		if ev.Type != Added {
			t.Errorf("replay event type = %v, want Added", ev.Type)
		}
		got[ev.Object.GetMeta().Name] = true
	}
	if !got["pre1"] || !got["pre2"] {
		t.Errorf("replay missing objects: %v", got)
	}

	// Live event after replay, with the next revision.
	s.Apply(pod("default", "live"), 0) // rev 3
	ev := recvOrFail(t, ch)
	if ev.Object.GetMeta().Name != "live" || ev.Revision != 3 {
		t.Errorf("live event = %s@%d, want live@3", ev.Object.GetMeta().Name, ev.Revision)
	}
}

func TestWatchClosesOnCancel(t *testing.T) {
	s := newMem(t)
	ctx, cancel := context.WithCancel(t.Context())
	ch, _ := s.Watch(ctx, "Pod", "", 0)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// may deliver buffered events first; drain until closed
			for range ch {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("watch channel not closed after cancel")
	}
}

func TestWALDurabilityAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neko.wal")
	s1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p := pod("default", "persist")
	p.Meta.Labels = map[string]string{"k": "v"}
	s1.Apply(p, 0)                            // rev 1
	up, _ := s1.Get(Key{"Pod", "default", "persist"})
	up.(*api.Pod).Status.Phase = "Running"
	s1.Apply(up, up.GetMeta().Revision)       // rev 2
	s1.Apply(pod("default", "gone"), 0)       // rev 3
	s1.Delete(Key{"Pod", "default", "gone"}, 3) // rev 4

	// Reopen: replay must rebuild exactly.
	s2, err := New(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.CurrentRevision() != 4 {
		t.Errorf("replayed revision = %d, want 4", s2.CurrentRevision())
	}
	got, ok := s2.Get(Key{"Pod", "default", "persist"})
	if !ok {
		t.Fatal("persisted pod missing after replay")
	}
	if got.(*api.Pod).Status.Phase != "Running" {
		t.Errorf("replayed phase = %q, want Running", got.(*api.Pod).Status.Phase)
	}
	if got.GetMeta().Labels["k"] != "v" {
		t.Error("replayed labels lost")
	}
	if _, ok := s2.Get(Key{"Pod", "default", "gone"}); ok {
		t.Error("deleted pod resurrected after replay")
	}
}

// TestConcurrentApplyRace hammers the store from many goroutines; run with -race.
// Every create must get a unique revision and all must land.
func TestConcurrentApplyRace(t *testing.T) {
	s := newMem(t)
	const n = 200
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			s.Apply(pod("default", name(i)), 0)
		})
	}
	// Concurrent readers to exercise RLock/Lock interleaving.
	for range 20 {
		wg.Go(func() {
			for range 50 {
				s.List("Pod", "default", nil)
			}
		})
	}
	wg.Wait()

	all, rev := s.List("Pod", "default", nil)
	if len(all) != n {
		t.Errorf("created %d pods, want %d", len(all), n)
	}
	if rev != n {
		t.Errorf("final revision = %d, want %d", rev, n)
	}
	seen := map[uint64]bool{}
	for _, o := range all {
		r := o.GetMeta().Revision
		if seen[r] {
			t.Errorf("duplicate revision %d", r)
		}
		seen[r] = true
	}
}

// helpers

func recvOrFail(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("watch channel closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return Event{}
	}
}

func names(objs []api.Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.GetMeta().Name
	}
	return out
}

func name(i int) string {
	return "p" + string(rune('A'+i%26)) + string(rune('0'+(i/26)%10)) + string(rune('0'+i/260))
}
