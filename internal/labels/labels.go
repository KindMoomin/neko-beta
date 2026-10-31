// Package labels is a minimal label-selector helper. Equality-only for now
// (M1); richer set-based selectors are deferred. Local, not k8s.io/apimachinery
// — we pull in that dependency only if/when a real need appears.
package labels

// Set is an object's labels.
type Set map[string]string

// Selector matches a label Set. Nil/empty Selector matches everything.
type Selector map[string]string

// Matches reports whether every key/value in the selector is present in the set.
func (s Selector) Matches(set Set) bool {
	for k, v := range s {
		if set[k] != v {
			return false
		}
	}
	return true
}

// Empty reports whether the selector constrains nothing (matches all).
func (s Selector) Empty() bool { return len(s) == 0 }
