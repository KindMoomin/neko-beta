package clientcmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalKind(t *testing.T) {
	cases := map[string]string{
		"pod": "Pod", "pods": "Pod", "po": "Pod", "Pod": "Pod",
		"svc": "Service", "services": "Service",
		"deploy": "Deployment", "ns": "Namespace", "rs": "ReplicaSet",
		"eps": "EndpointSlice",
		// Unknown input passes through so the server gives the authoritative error.
		"widget": "widget",
	}
	for in, want := range cases {
		if got := canonicalKind(in); got != want {
			t.Errorf("canonicalKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderListPodColumns(t *testing.T) {
	// Two pods, one Running with an IP, one Pending with none.
	items := []json.RawMessage{
		json.RawMessage(`{"metadata":{"name":"a","namespace":"default"},"status":{"phase":"Running","podIP":"10.244.0.2","ready":true}}`),
		json.RawMessage(`{"metadata":{"name":"b","namespace":"default"},"status":{"phase":"Pending"}}`),
	}
	var buf bytes.Buffer
	c := &Client{out: &buf}
	if err := c.renderList("Pod", items); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"PHASE", "IP", "READY", "Running", "10.244.0.2", "true", "Pending", "false"} {
		if !strings.Contains(out, want) {
			t.Errorf("pod table missing %q; got:\n%s", want, out)
		}
	}
	// The Pending pod's empty IP renders as a dash.
	if !strings.Contains(out, "-") {
		t.Errorf("expected a dash for the empty IP; got:\n%s", out)
	}
}

func TestRenderListServiceColumns(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"metadata":{"name":"web","namespace":"default"},"spec":{"clusterIP":"10.96.0.10"}}`),
	}
	var buf bytes.Buffer
	c := &Client{out: &buf}
	if err := c.renderList("Service", items); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"CLUSTERIP", "10.96.0.10", "web"} {
		if !strings.Contains(out, want) {
			t.Errorf("service table missing %q; got:\n%s", want, out)
		}
	}
}
