package controller_test

import (
	"testing"
	"time"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/controller"
	"github.com/neko-box/neko/internal/controller/deployment"
	"github.com/neko-box/neko/internal/controller/replicaset"
	"github.com/neko-box/neko/internal/store"
)

// TestDeploymentConvergesToPods drives the real controller runtime: applying a
// Deployment should produce a ReplicaSet and the right number of Pods.
func TestDeploymentConvergesToPods(t *testing.T) {
	st, err := store.New("")
	if err != nil {
		t.Fatal(err)
	}
	acc := &access.InProc{S: st}
	ctx := t.Context()

	go controller.Run(ctx, acc,
		&deployment.Reconciler{Client: acc},
		&replicaset.Reconciler{Client: acc},
	)

	dep := &api.Deployment{
		TypeMeta: api.TypeMeta{Kind: "Deployment", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: api.DeploymentSpec{
			Replicas: 3,
			Selector: map[string]string{"app": "web"},
			Template: api.PodSpec{Containers: []api.Container{{Name: "c", Image: "nginx"}}},
		},
	}
	if _, err := acc.Apply(ctx, dep); err != nil {
		t.Fatal(err)
	}

	// A ReplicaSet named web-rs should appear.
	waitFor(t, "replicaset created", func() bool {
		_, ok, _ := acc.Get(ctx, store.Key{Kind: "ReplicaSet", Namespace: "default", Name: "web-rs"})
		return ok
	})
	// And exactly 3 Pods owned by it.
	waitFor(t, "3 pods", func() bool { return countPods(t, acc) == 3 })

	// Scale down to 1: excess pods are deleted.
	cur, _, _ := acc.Get(ctx, store.Key{Kind: "Deployment", Namespace: "default", Name: "web"})
	d := cur.(*api.Deployment)
	d.Spec.Replicas = 1
	if _, err := acc.Apply(ctx, d); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "scaled to 1 pod", func() bool { return countPods(t, acc) == 1 })

	// Scale up to 2.
	cur, _, _ = acc.Get(ctx, store.Key{Kind: "Deployment", Namespace: "default", Name: "web"})
	d = cur.(*api.Deployment)
	d.Spec.Replicas = 2
	if _, err := acc.Apply(ctx, d); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "scaled to 2 pods", func() bool { return countPods(t, acc) == 2 })

	// Pods carry the RS owner ref and the selector labels.
	objs, _, _ := acc.List(ctx, "Pod", "default", nil)
	for _, o := range objs {
		p := o.(*api.Pod)
		if p.Meta.Labels["app"] != "web" {
			t.Errorf("pod %s missing selector label", p.Meta.Name)
		}
		if len(p.Meta.OwnerRefs) == 0 || p.Meta.OwnerRefs[0].Kind != "ReplicaSet" {
			t.Errorf("pod %s missing ReplicaSet owner ref", p.Meta.Name)
		}
	}
}

// TestSelfHealRecreatesDeletedPod: deleting a Pod under a ReplicaSet triggers a
// recreate (level-triggered convergence).
func TestSelfHealRecreatesDeletedPod(t *testing.T) {
	st, _ := store.New("")
	acc := &access.InProc{S: st}
	ctx := t.Context()
	go controller.Run(ctx, acc, &replicaset.Reconciler{Client: acc})

	rs := &api.ReplicaSet{
		TypeMeta: api.TypeMeta{Kind: "ReplicaSet", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: "rs", Namespace: "default", UID: "rs-uid"},
		Spec: api.ReplicaSetSpec{
			Replicas: 2,
			Selector: map[string]string{"app": "x"},
			Template: api.PodSpec{Containers: []api.Container{{Name: "c", Image: "busybox"}}},
		},
	}
	acc.Apply(ctx, rs)
	waitFor(t, "2 pods", func() bool { return countPods(t, acc) == 2 })

	// Delete one pod; the RS should recreate it.
	objs, _, _ := acc.List(ctx, "Pod", "default", nil)
	p := objs[0].(*api.Pod)
	acc.Delete(ctx, store.Key{Kind: "Pod", Namespace: "default", Name: p.Meta.Name}, p.Meta.Revision)
	waitFor(t, "recovered to 2 pods", func() bool { return countPods(t, acc) == 2 })
}

func countPods(t *testing.T, acc *access.InProc) int {
	t.Helper()
	objs, _, err := acc.List(t.Context(), "Pod", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(objs)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
