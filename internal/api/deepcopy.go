package api

import "maps"

// Deep-copy helpers. The store must never hand out a pointer that aliases its
// internal object, or a caller could mutate stored state without going through
// Apply (breaking the single-writer / optimistic-concurrency invariant — see
// docs risk #2). Every DeepCopyObject returns a fully independent value.

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

func copyMeta(m ObjectMeta) ObjectMeta {
	m.Labels = copyStringMap(m.Labels)
	if m.OwnerRefs != nil {
		refs := make([]OwnerRef, len(m.OwnerRefs))
		copy(refs, m.OwnerRefs)
		m.OwnerRefs = refs
	}
	return m
}

func copyContainers(cs []Container) []Container {
	if cs == nil {
		return nil
	}
	out := make([]Container, len(cs))
	for i, c := range cs {
		if c.Ports != nil {
			ports := make([]int, len(c.Ports))
			copy(ports, c.Ports)
			c.Ports = ports
		}
		out[i] = c
	}
	return out
}

func copyPodSpec(s PodSpec) PodSpec {
	s.Containers = copyContainers(s.Containers)
	return s
}
