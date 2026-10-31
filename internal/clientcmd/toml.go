package clientcmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/neko-box/neko/internal/api"
)

// decodeTOMLFile reads a neko-native TOML object file and returns the concrete
// api object. The TOML must carry a top-level `kind`.
//
// Approach: decode TOML into a generic map, then round-trip through JSON so the
// api types' existing `json` tags are the single source of truth for field
// mapping — no parallel set of `toml` tags to keep in sync.
func decodeTOMLFile(path string) (api.Object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse TOML %s: %w", path, err)
	}
	kind, _ := raw["kind"].(string)
	if kind == "" {
		return nil, fmt.Errorf("%s: missing top-level `kind`", path)
	}
	obj, err := api.New(kind)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// TOML map -> JSON bytes -> api type (json tags do the field mapping).
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(j, obj); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if obj.GetMeta().Name == "" {
		return nil, fmt.Errorf("%s: object has no metadata.name", path)
	}
	return obj, nil
}
