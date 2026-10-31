// Package litterbox is the node agent's pod lifecycle loop (neko-box's minimal
// kubelet). It watches Pods bound to this node (via access.Watcher) and
// reconciles each to a running container.
//
// Networking model (M1): pod networking is handled by containerd's CRI plugin
// driving the CNI conflist that `neko up` installs — NOT by hand here. So the
// litterbox's job is: on a bound Pod, create the sandbox (containerd+CNI assign
// the IP), start its container(s), and write PodIP + phase back to the store. It
// is level-triggered: each wake re-syncs desired (store) against actual (its own
// tracked sandboxes), so a missed watch event self-heals.
package litterbox

import (
	"context"
	"log"
	"sync"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/cri"
	"github.com/neko-box/neko/internal/store"
)

// Litterbox runs pods bound to NodeName on this node.
type Litterbox struct {
	nodeName string
	client   access.StoreAccess
	runtime  *cri.Runtime

	mu        sync.Mutex
	sandboxes map[string]*cri.Sandbox // pod UID -> running sandbox
	busy      map[string]bool         // pod UID -> reconcile in flight
}

// New constructs a litterbox for one node.
func New(nodeName string, client access.StoreAccess, rt *cri.Runtime) *Litterbox {
	return &Litterbox{
		nodeName:  nodeName,
		client:    client,
		runtime:   rt,
		sandboxes: make(map[string]*cri.Sandbox),
		busy:      make(map[string]bool),
	}
}

// Run watches Pods and reconciles those bound to this node until ctx is done.
func (k *Litterbox) Run(ctx context.Context) error {
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

// handle reconciles one watch event for a Pod. It dispatches the actual work to
// a per-pod goroutine so that a slow pod (image pull, sandbox create) never
// blocks the watch loop and stalls other pods (head-of-line blocking). A per-UID
// "busy" guard ensures only one reconcile runs per pod at a time.
func (k *Litterbox) handle(ctx context.Context, ev store.Event) {
	pod, ok := ev.Object.(*api.Pod)
	if !ok {
		return
	}
	log.Printf("litterbox DEBUG: event type=%d pod=%s node=%q phase=%q", ev.Type, pod.Meta.Name, pod.Spec.NodeName, pod.Status.Phase)
	// Only act on pods bound to this node.
	if pod.Spec.NodeName != k.nodeName {
		return
	}
	if ev.Type == store.Deleted {
		go k.teardown(ctx, pod)
		return
	}
	// Coalesce concurrent reconciles for the same pod.
	k.mu.Lock()
	if k.busy[pod.Meta.UID] {
		k.mu.Unlock()
		return
	}
	k.busy[pod.Meta.UID] = true
	k.mu.Unlock()

	go func() {
		defer func() {
			k.mu.Lock()
			delete(k.busy, pod.Meta.UID)
			k.mu.Unlock()
		}()
		if err := k.ensureRunning(ctx, pod); err != nil {
			log.Printf("litterbox: pod %s/%s: %v", pod.Meta.Namespace, pod.Meta.Name, err)
		}
	}()
}

// ensureRunning brings a bound Pod up to Running if it isn't already.
func (k *Litterbox) ensureRunning(ctx context.Context, pod *api.Pod) error {
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
func (k *Litterbox) teardown(ctx context.Context, pod *api.Pod) {
	k.mu.Lock()
	sb := k.sandboxes[pod.Meta.UID]
	delete(k.sandboxes, pod.Meta.UID)
	k.mu.Unlock()
	if sb == nil {
		return
	}
	if err := k.runtime.Stop(ctx, sb.ID); err != nil {
		log.Printf("litterbox: teardown %s/%s: %v", pod.Meta.Namespace, pod.Meta.Name, err)
	}
}

// writeStatus applies a status update to the Pod via optimistic concurrency,
// retrying once on conflict by re-reading the latest revision.
func (k *Litterbox) writeStatus(ctx context.Context, pod *api.Pod, phase, podIP string, ready bool) error {
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
