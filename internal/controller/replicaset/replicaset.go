// Package replicaset reconciles ReplicaSets into Pods: it keeps exactly
// Spec.Replicas Pods that this ReplicaSet owns, creating or deleting to converge.
// New Pods are created unbound; the node-agent binder (placement, D5) assigns
// NodeName, which is what makes the litterbox run them. Level-triggered — counts
// actual Pods each pass, so a crashed/duplicated event self-heals.
package replicaset

import (
	"context"
	"fmt"
	"sort"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/store"
)

// Reconciler owns ReplicaSet -> Pod count.
type Reconciler struct {
	Client access.StoreAccess
}

func (r *Reconciler) Name() string    { return "replicaset" }
func (r *Reconciler) Kinds() []string { return []string{"ReplicaSet", "Pod"} }

// MapToReconcileKey resolves the ReplicaSet a change event is about. A
// ReplicaSet event maps to itself; a Pod event maps to its owning ReplicaSet,
// read from the EVENT OBJECT (so a delete tombstone still carries the owner —
// a fresh Get would fail for a just-deleted pod).
func (r *Reconciler) MapToReconcileKey(ev store.Event) (store.Key, bool) {
	m := ev.Object.GetMeta()
	switch ev.Object.GetType().Kind {
	case "ReplicaSet":
		return store.Key{Kind: "ReplicaSet", Namespace: m.Namespace, Name: m.Name}, true
	case "Pod":
		for _, o := range m.OwnerRefs {
			if o.Kind == "ReplicaSet" {
				return store.Key{Kind: "ReplicaSet", Namespace: m.Namespace, Name: o.Name}, true
			}
		}
	}
	return store.Key{}, false
}

func (r *Reconciler) Reconcile(ctx context.Context, key store.Key) error {
	if key.Kind != "ReplicaSet" {
		return nil
	}
	obj, ok, err := r.Client.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return nil // RS gone; its pods are cleaned up by cascade/GC (namespace controller / later)
	}
	rs := obj.(*api.ReplicaSet)

	owned, err := r.ownedPods(ctx, rs)
	if err != nil {
		return err
	}

	switch {
	case len(owned) < rs.Spec.Replicas:
		// Create pods at whichever indices [0, Replicas) are currently missing,
		// rather than assuming owned pods occupy a contiguous 0..n-1 range (a
		// deleted middle pod must be recreated at its own index).
		have := make(map[string]bool, len(owned))
		for _, p := range owned {
			have[p.Meta.Name] = true
		}
		created := 0
		need := rs.Spec.Replicas - len(owned)
		for i := 0; created < need; i++ {
			if have[podName(rs, i)] {
				continue
			}
			if err := r.createPod(ctx, rs, i); err != nil {
				return err
			}
			created++
		}
	case len(owned) > rs.Spec.Replicas:
		// Delete the newest-named excess deterministically.
		sort.Slice(owned, func(i, j int) bool { return owned[i].Meta.Name < owned[j].Meta.Name })
		for _, p := range owned[rs.Spec.Replicas:] {
			if err := r.Client.Delete(ctx, store.Key{Kind: "Pod", Namespace: p.Meta.Namespace, Name: p.Meta.Name}, p.Meta.Revision); err != nil && err != store.ErrNotFound {
				return err
			}
		}
	}
	return nil
}

// ownedPods lists Pods in the RS namespace that this RS owns.
func (r *Reconciler) ownedPods(ctx context.Context, rs *api.ReplicaSet) ([]*api.Pod, error) {
	objs, _, err := r.Client.List(ctx, "Pod", rs.Meta.Namespace, nil)
	if err != nil {
		return nil, err
	}
	var owned []*api.Pod
	for _, o := range objs {
		p := o.(*api.Pod)
		if p.Meta.Deleting {
			continue
		}
		for _, ref := range p.Meta.OwnerRefs {
			if ref.Kind == "ReplicaSet" && ref.Name == rs.Meta.Name {
				owned = append(owned, p)
				break
			}
		}
	}
	return owned, nil
}

// podName is the deterministic name of a ReplicaSet's pod at an index.
func podName(rs *api.ReplicaSet, index int) string {
	return fmt.Sprintf("%s-%d", rs.Meta.Name, index)
}

// createPod materializes one Pod from the RS template, owned by the RS. Pods are
// created unbound (NodeName empty); the binder assigns a node.
func (r *Reconciler) createPod(ctx context.Context, rs *api.ReplicaSet, index int) error {
	name := podName(rs, index)
	pod := &api.Pod{
		TypeMeta: api.TypeMeta{Kind: "Pod", APIVersion: api.APIVersion},
		Meta: api.ObjectMeta{
			Name:      name,
			Namespace: rs.Meta.Namespace,
			Labels:    rs.Spec.Selector,
			OwnerRefs: []api.OwnerRef{{Kind: "ReplicaSet", Name: rs.Meta.Name, UID: rs.Meta.UID}},
		},
		Spec: rs.Spec.Template,
	}
	// Index-named pods make create idempotent: if it already exists, skip.
	if _, ok, err := r.Client.Get(ctx, store.Key{Kind: "Pod", Namespace: pod.Meta.Namespace, Name: name}); err != nil {
		return err
	} else if ok {
		return nil
	}
	_, err := r.Client.Apply(ctx, pod) // create
	if err == store.ErrExists {
		return nil
	}
	return err
}
