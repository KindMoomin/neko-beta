package placement

import (
	"testing"

	"github.com/neko-box/neko/internal/api"
)

func pod(uid string) *api.Pod {
	return &api.Pod{Meta: api.ObjectMeta{UID: uid}}
}

func TestPickLeastPods(t *testing.T) {
	nodes := []NodeView{
		{Name: "a", PodCount: 5, Ready: true},
		{Name: "b", PodCount: 2, Ready: true},
		{Name: "c", PodCount: 9, Ready: true},
	}
	got, err := Pick(pod("x"), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "b" {
		t.Errorf("Pick = %q, want b (fewest pods)", got)
	}
}

func TestPickSkipsNotReady(t *testing.T) {
	nodes := []NodeView{
		{Name: "a", PodCount: 0, Ready: false}, // fewest but not ready
		{Name: "b", PodCount: 3, Ready: true},
	}
	got, err := Pick(pod("x"), nodes)
	if err != nil || got != "b" {
		t.Errorf("Pick = %q,%v, want b,nil", got, err)
	}
}

func TestPickNoReadyNode(t *testing.T) {
	_, err := Pick(pod("x"), []NodeView{{Name: "a", Ready: false}})
	if err != ErrNoNode {
		t.Errorf("err = %v, want ErrNoNode", err)
	}
}

func TestPickTieIsDeterministic(t *testing.T) {
	nodes := []NodeView{
		{Name: "a", PodCount: 1, Ready: true},
		{Name: "b", PodCount: 1, Ready: true},
	}
	first, _ := Pick(pod("same-uid"), nodes)
	for range 10 {
		got, _ := Pick(pod("same-uid"), nodes)
		if got != first {
			t.Fatalf("tie-break not deterministic: %q vs %q", got, first)
		}
	}
}
