// Package kubelet is the node agent's pod lifecycle loop. It watches Pods bound
// to this node (via access.Watcher) and reconciles each to a running container.
//
// Networking model (M1): pod networking is handled by containerd's CRI plugin
// driving the CNI conflist that `neko up` installs — NOT by hand here. So the
// kubelet's job is: on a bound Pod, create the sandbox (containerd+CNI assign the
// IP), start its container(s), and write PodIP + phase back to the store. It is
// level-triggered: each wake re-syncs desired (store) against actual (its own
// tracked sandboxes), so a missed watch event self-heals.
package kubelet

import (
	"context"
	"log"
	"sync"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/cri"
	"github.com/neko-box/neko/internal/store"
)

// Kubelet runs pods bound to NodeName on this node.
type Kubelet struct {
	nodeName string
	client   access.StoreAccess
	runtime  *cri.Runtime

	mu        sync.Mutex
	sandboxes map[string]*cri.Sandbox // pod UID -> running sandbox
}

// New constructs a kubelet for one node.
func New(nodeName string, client access.StoreAccess, rt *cri.Runtime) *Kubelet {
	return &Kubelet{
		nodeName:  nodeName,
		client:    client,
		runtime:   rt,
		sandboxes: make(map[string]*cri.Sandbox),
	}
}

// Run watches Pods and reconciles those bound to this node until ctx is done.
func (k *Kubelet) Run(ctx context.Context) error {
	ch, err := k.client.Watch(ctx, "Pod", "", 0) // all namespaces, replay+tail
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				// Watch closed (e.g. lagged) — relist and resubscribe.
				ch, err = k.client.Watch(ctx, "Pod", "", 0)
				if err != nil {
					return err
				}
				continue
			}
			k.handle(ctx, ev)
		}
	}
}

// handle reconciles one watch event for a Pod.
func (k *Kubelet) handle(ctx context.Context, ev store.Event) {
	pod, ok := ev.Object.(*api.Pod)
	if !ok {
		return
	}
	// Only act on pods bound to this node.
	if pod.Spec.NodeName != k.nodeName {
		return
	}
	switch ev.Type {
	case store.Deleted:
		k.teardown(ctx, pod)
	default:
		if err := k.ensureRunning(ctx, pod); err != nil {
			log.Printf("kubelet: pod %s/%s: %v", pod.Meta.Namespace, pod.Meta.Name, err)
		}
	}
}

// ensureRunning brings a bound Pod up to Running if it isn't already.
func (k *Kubelet) ensureRunning(ctx context.Context, pod *api.Pod) error {
	k.mu.Lock()
	_, already := k.sandboxes[pod.Meta.UID]
	k.mu.Unlock()
	if already || pod.Status.Phase == "Running" {
		return nil
	}

	// Create the sandbox — containerd + the installed CNI conflist assign the IP.
	sb, err := k.runtime.CreateSandbox(ctx, pod, "")
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.sandboxes[pod.Meta.UID] = sb
	k.mu.Unlock()

	// Pull + start each container.
	for _, c := range pod.Spec.Containers {
		if err := k.runtime.EnsureImage(ctx, c.Image); err != nil {
			return err
		}
		if _, err := k.runtime.StartContainer(ctx, sb, c); err != nil {
			return err
		}
	}

	// Discover the sandbox IP and report Running.
	ip, _ := k.runtime.SandboxIP(ctx, sb.ID)
	return k.writeStatus(ctx, pod, "Running", ip, true)
}

// teardown stops the sandbox for a deleted Pod.
func (k *Kubelet) teardown(ctx context.Context, pod *api.Pod) {
	k.mu.Lock()
	sb := k.sandboxes[pod.Meta.UID]
	delete(k.sandboxes, pod.Meta.UID)
	k.mu.Unlock()
	if sb == nil {
		return
	}
	if err := k.runtime.Stop(ctx, sb.ID); err != nil {
		log.Printf("kubelet: teardown %s/%s: %v", pod.Meta.Namespace, pod.Meta.Name, err)
	}
}

// writeStatus applies a status update to the Pod via optimistic concurrency,
// retrying once on conflict by re-reading the latest revision.
func (k *Kubelet) writeStatus(ctx context.Context, pod *api.Pod, phase, podIP string, ready bool) error {
	for range 2 {
		cur, ok, err := k.client.Get(ctx, store.Key{Kind: "Pod", Namespace: pod.Meta.Namespace, Name: pod.Meta.Name})
		if err != nil {
			return err
		}
		if !ok {
			return nil // pod deleted meanwhile
		}
		p := cur.(*api.Pod)
		p.Status.Phase = phase
		p.Status.PodIP = podIP
		p.Status.Ready = ready
		if _, err := k.client.Apply(ctx, p); err != nil {
			if err == store.ErrConflict {
				continue // re-read and retry
			}
			return err
		}
		return nil
	}
	return store.ErrConflict
}
