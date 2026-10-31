// Package placement decides which node a pod binds to. Pure functions, no I/O —
// this is the whole of what neko-box keeps from kube-scheduler (see docs: D5).
package placement

import (
	"errors"
	"hash/fnv"

	"github.com/neko-box/neko/internal/api"
)

// NodeView is placement's read-only view of a candidate node.
type NodeView struct {
	Name     string
	PodCount int
	Ready    bool
}

// ErrNoNode is returned when no ready node can host the pod.
var ErrNoNode = errors.New("placement: no ready node available")

// Pick returns the node a pod should bind to. Among ready nodes it chooses the
// one with the fewest pods; ties are broken deterministically by a hash of the
// pod UID so the same pod always lands the same way (idempotent reconcile).
func Pick(pod *api.Pod, nodes []NodeView) (string, error) {
	best := -1
	for i, n := range nodes {
		if !n.Ready {
			continue
		}
		switch {
		case best < 0:
			best = i
		case n.PodCount < nodes[best].PodCount:
			best = i
		case n.PodCount == nodes[best].PodCount:
			// Tie: pick deterministically by UID hash parity against position.
			if tieBreak(pod.Meta.UID, n.Name) < tieBreak(pod.Meta.UID, nodes[best].Name) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", ErrNoNode
	}
	return nodes[best].Name, nil
}

func tieBreak(uid, node string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uid))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(node))
	return h.Sum64()
}

