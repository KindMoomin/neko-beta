package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/store"
)

// testEvent decodes a watch frame; Object is raw because api.Object is an
// interface json can't populate directly.
type testEvent struct {
	Type     string          `json:"type"`
	Revision uint64          `json:"revision"`
	Object   json.RawMessage `json:"object"`
}

func (e testEvent) podName(t *testing.T) string {
	t.Helper()
	var p api.Pod
	if err := json.Unmarshal(e.Object, &p); err != nil {
		t.Fatalf("decode watch object: %v", err)
	}
	return p.Meta.Name
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := store.New("")
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(New(s, nil).Routes())
}

func podJSON(name string) []byte {
	p := api.Pod{
		TypeMeta: api.TypeMeta{Kind: "Pod", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: name},
		Spec:     api.PodSpec{Containers: []api.Container{{Name: "c", Image: "nginx"}}},
	}
	b, _ := json.Marshal(p)
	return b
}

func TestCRUDLifecycle(t *testing.T) {
	ts := newServer(t)
	defer ts.Close()
	c := ts.Client()

	// CREATE
	resp, err := c.Post(ts.URL+"/v1/Pod/default", "application/json", bytes.NewReader(podJSON("web")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created api.Pod
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Meta.Revision != 1 {
		t.Errorf("created revision = %d, want 1", created.Meta.Revision)
	}
	if created.Meta.Namespace != "default" {
		t.Errorf("namespace = %q, want default (from path)", created.Meta.Namespace)
	}

	// GET
	resp, _ = c.Get(ts.URL + "/v1/Pod/default/web")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// GET missing -> 404
	resp, _ = c.Get(ts.URL + "/v1/Pod/default/ghost")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// LIST
	resp, _ = c.Get(ts.URL + "/v1/Pod")
	var lr struct {
		Items    []json.RawMessage `json:"items"`
		Revision uint64            `json:"revision"`
	}
	json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if len(lr.Items) != 1 {
		t.Errorf("list items = %d, want 1", len(lr.Items))
	}

	// REPLACE with correct rev -> 200
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/Pod/default/web?rev=1", bytes.NewReader(podJSON("web")))
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// REPLACE with stale rev -> 409
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/v1/Pod/default/web?rev=1", bytes.NewReader(podJSON("web")))
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("stale replace = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE with correct rev (now 2) -> 204
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/v1/Pod/default/web?rev=2", nil)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateDuplicateConflict(t *testing.T) {
	ts := newServer(t)
	defer ts.Close()
	c := ts.Client()
	c.Post(ts.URL+"/v1/Pod/default", "application/json", bytes.NewReader(podJSON("web")))
	resp, _ := c.Post(ts.URL+"/v1/Pod/default", "application/json", bytes.NewReader(podJSON("web")))
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHealthz(t *testing.T) {
	ts := newServer(t)
	defer ts.Close()
	resp, _ := ts.Client().Get(ts.URL + "/v1/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["ready"] != true {
		t.Errorf("healthz ready = %v, want true", body["ready"])
	}
}

func TestWatchStreamsReplayAndLive(t *testing.T) {
	ts := newServer(t)
	defer ts.Close()
	c := ts.Client()

	// Pre-existing object to be replayed.
	c.Post(ts.URL+"/v1/Pod/default", "application/json", bytes.NewReader(podJSON("pre")))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/Pod?watch=1", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch status = %d, want 200", resp.StatusCode)
	}

	events := make(chan testEvent, 4)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var ev testEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				events <- ev
			}
		}
		_ = sc.Err() // stream closed (ctx cancel / EOF); nothing to assert here
	}()

	// Replay of "pre".
	ev := recv(t, events)
	if ev.Type != "ADDED" || ev.podName(t) != "pre" {
		t.Errorf("replay event = %s %s, want ADDED pre", ev.Type, ev.podName(t))
	}

	// Live create arrives on the stream.
	c.Post(ts.URL+"/v1/Pod/default", "application/json", bytes.NewReader(podJSON("live")))
	ev = recv(t, events)
	if ev.Type != "ADDED" || ev.podName(t) != "live" {
		t.Errorf("live event = %s %s, want ADDED live", ev.Type, ev.podName(t))
	}
}

func recv(t *testing.T, ch <-chan testEvent) testEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return testEvent{}
	}
}
