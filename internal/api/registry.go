package api

import "fmt"

// New returns a fresh zero-valued object for a kind, or an error for an unknown
// kind. It is the single place that maps a kind string to a concrete type — used
// by the WAL (replay) and httpapi (request decode) so neither duplicates the map.
func New(kind string) (Object, error) {
	switch kind {
	case "Pod":
		return &Pod{}, nil
	case "Namespace":
		return &Namespace{}, nil
	case "Deployment":
		return &Deployment{}, nil
	case "ReplicaSet":
		return &ReplicaSet{}, nil
	case "Service":
		return &Service{}, nil
	case "EndpointSlice":
		return &EndpointSlice{}, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
}
