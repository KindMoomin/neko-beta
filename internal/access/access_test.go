package access

import (
	"testing"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/store"
)

func TestInProcRoundTrip(t *testing.T) {
	s, err := store.New("")
	if err != nil {
		t.Fatal(err)
	}
	a := &InProc{S: s}
	ctx := t.Context()

	p := &api.Pod{
		TypeMeta: api.TypeMeta{Kind: "Pod", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: "a", Namespace: "default"},
	}
	created, err := a.Apply(ctx, p) // revision 0 on the object => create
	if err != nil {
		t.Fatalf("apply create: %v", err)
	}
	if created.GetMeta().Revision != 1 {
		t.Errorf("revision = %d, want 1", created.GetMeta().Revision)
	}

	got, ok, err := a.Get(ctx, store.Key{Kind: "Pod", Namespace: "default", Name: "a"})
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}

	// Update carries the current revision on the object (CAS).
	got.GetMeta().Revision = created.GetMeta().Revision
	if _, err := a.Apply(ctx, got); err != nil {
		t.Fatalf("apply update: %v", err)
	}

	list, rev, err := a.List(ctx, "Pod", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || rev != 2 {
		t.Errorf("list len=%d rev=%d, want 1,2", len(list), rev)
	}

	if err := a.Delete(ctx, store.Key{Kind: "Pod", Namespace: "default", Name: "a"}, rev); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := a.Get(ctx, store.Key{Kind: "Pod", Namespace: "default", Name: "a"}); ok {
		t.Error("pod present after delete")
	}
}
