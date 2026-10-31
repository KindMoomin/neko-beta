// Package deployment reconciles Deployments into ReplicaSets: each Deployment
// owns one ReplicaSet named "<deployment>-rs" carrying the same replica count,
// selector, and pod template. Level-triggered — reads desired+actual each pass.
package deployment

import (
	"context"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns Deployment -> ReplicaSet.
type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string    { return "deployment" }
func (r *Reconciler) Kinds() []string { return []string{"Deployment"} }

// MapToReconcileKey: Deployment events reconcile themselves.
func (r *Reconciler) MapToReconcileKey(ev store.Event) (store.Key, bool) {
	m := ev.Object.GetMeta()
	return store.Key{Kind: "Deployment", Namespace: m.Namespace, Name: m.Name}, true
}

// rsName is the deterministic name of a Deployment's ReplicaSet.
func rsName(deployment string) string { return deployment + "-rs" }

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	// Only Deployment keys drive this controller.
	if key.Kind != "Deployment" {
		return nil
	}
	obj, ok, err := r.Client.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return nil // deleted; ReplicaSet cascade handled elsewhere (namespace/GC later)
	}
	d := obj.(*api.Deployment)

	want := &api.ReplicaSet{
		TypeMeta: api.TypeMeta{Kind: "ReplicaSet", APIVersion: api.APIVersion},
		Meta: api.ObjectMeta{
			Name:      rsName(d.Meta.Name),
			Namespace: d.Meta.Namespace,
			Labels:    d.Spec.Selector,
			OwnerRefs: []api.OwnerRef{{Kind: "Deployment", Name: d.Meta.Name, UID: d.Meta.UID}},
		},
		Spec: api.ReplicaSetSpec{
			Replicas: d.Spec.Replicas,
			Selector: d.Spec.Selector,
			Template: d.Spec.Template,
		},
	}

	cur, ok, err := r.Client.Get(ctx, store.Key{Kind: "ReplicaSet", Namespace: d.Meta.Namespace, Name: rsName(d.Meta.Name)})
	if err != nil {
		return err
	}
	if !ok {
		_, err := r.Client.Apply(ctx, want) // create (revision 0)
		return err
	}
	// Update in place if spec drifted, preserving the current revision for CAS.
	rs := cur.(*api.ReplicaSet)
	if rs.Spec.Replicas != want.Spec.Replicas || !sameTemplate(rs.Spec.Template, want.Spec.Template) {
		rs.Spec = want.Spec
		_, err := r.Client.Apply(ctx, rs)
		return err
	}
	return nil
}

// sameTemplate compares the parts of a pod template the Deployment owns.
func sameTemplate(a, b api.PodSpec) bool {
	if len(a.Containers) != len(b.Containers) {
		return false
	}
	for i := range a.Containers {
		if a.Containers[i].Image != b.Containers[i].Image || a.Containers[i].Name != b.Containers[i].Name {
			return false
		}
	}
	return true
}
