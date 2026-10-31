package clientcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neko-box/neko/internal/api"
)

func TestDecodeTOMLPod(t *testing.T) {
	src := `
kind = "Pod"
apiVersion = "neko/v1"

[metadata]
name = "hello"
namespace = "web"

[metadata.labels]
app = "hello"

[[spec.containers]]
name = "c"
image = "nginx:alpine"
ports = [80, 443]
`
	path := filepath.Join(t.TempDir(), "pod.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	obj, err := decodeTOMLFile(path)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	p, ok := obj.(*api.Pod)
	if !ok {
		t.Fatalf("decoded type = %T, want *api.Pod", obj)
	}
	if p.Meta.Name != "hello" || p.Meta.Namespace != "web" {
		t.Errorf("meta = %q/%q, want web/hello", p.Meta.Namespace, p.Meta.Name)
	}
	if p.Meta.Labels["app"] != "hello" {
		t.Errorf("label app = %q, want hello", p.Meta.Labels["app"])
	}
	if len(p.Spec.Containers) != 1 || p.Spec.Containers[0].Image != "nginx:alpine" {
		t.Fatalf("containers = %+v", p.Spec.Containers)
	}
	if got := p.Spec.Containers[0].Ports; len(got) != 2 || got[0] != 80 || got[1] != 443 {
		t.Errorf("ports = %v, want [80 443]", got)
	}
}

func TestDecodeTOMLErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no kind":    "[metadata]\nname = \"x\"\n",
		"bad kind":   "kind = \"Widget\"\n[metadata]\nname = \"x\"\n",
		"no name":    "kind = \"Pod\"\n[metadata]\nnamespace = \"d\"\n",
		"bad toml":   "kind = \"Pod\"\n[metadata\nname =",
	}
	for label, src := range cases {
		path := filepath.Join(dir, "x.toml")
		os.WriteFile(path, []byte(src), 0o600)
		if _, err := decodeTOMLFile(path); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}
