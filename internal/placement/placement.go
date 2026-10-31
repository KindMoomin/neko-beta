// Package placement decides which node a pod binds to. Pure functions, no I/O —
// this is the whole of what neko-box keeps from kube-scheduler (see docs: D5).
package placement

import "github.com/neko-box/neko/internal/api"

// NodeView is placement's read-only view of a candidate node.
type NodeView struct {
	Name     string
	PodCount int
	Ready    bool
}

// Pick returns the node a pod should bind to. Single ready node => that node;
// multiple => least-pods, ties broken deterministically by UID hash.
func Pick(pod *api.Pod, nodes []NodeView) (nodeName string, err error) {
	panic("not implemented")
}
