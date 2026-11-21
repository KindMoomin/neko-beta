// Package clientcmd is the nekoctl client: it talks to the node agent's httpapi
// over HTTP, decoding neko-native TOML locally and rendering get/delete/logs.
// This is the only StoreAccess consumer that is always remote.
package clientcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/neko-box/neko/internal/api"
)

// Client is nekoctl's HTTP client to a neko-box node agent.
type Client struct {
	base string
	http *http.Client
	out  io.Writer
}

// New builds a client for the node agent at addr (e.g. "127.0.0.1:7443").
func New(addr string) *Client {
	return &Client{
		base: "http://" + addr,
		http: &http.Client{Timeout: 10 * time.Second},
		out:  os.Stdout,
	}
}

// Apply decodes a neko-native TOML file into an api object and applies it. The
// TOML must carry a top-level `kind`. Create vs replace is chosen by whether the
// object already exists (a GET probe), so `apply` is idempotent from the CLI.
func (c *Client) Apply(ctx context.Context, tomlPath string) error {
	obj, err := decodeTOMLFile(tomlPath)
	if err != nil {
		return err
	}
	m := obj.GetMeta()
	kind := obj.GetType().Kind
	ns := m.Namespace
	if ns == "" {
		ns = "default"
	}

	// Probe for an existing object to decide POST (create) vs PUT (replace).
	existing, ok, err := c.getObject(ctx, kind, ns, m.Name)
	if err != nil {
		return err
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	var req *http.Request
	if !ok {
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost,
			c.base+"/v1/"+kind+"/"+ns, bytes.NewReader(body))
	} else {
		rev := existing.GetMeta().Revision
		u := fmt.Sprintf("%s/v1/%s/%s/%s?rev=%d", c.base, kind, ns, m.Name, rev)
		req, _ = http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return httpError(resp)
	}
	verb := "created"
	if ok {
		verb = "configured"
	}
	fmt.Fprintf(c.out, "%s/%s %s\n", kind, m.Name, verb)
	return nil
}

// Get fetches and renders objects of a kind (ns=="" lists all namespaces).
// Get lists objects of a kind and renders them. The kind is canonicalized so
// `pod`, `pods`, and `po` all resolve to `Pod` (the single on-wire spelling).
func (c *Client) Get(ctx context.Context, kind, ns string) error {
	kind = canonicalKind(kind)
	u := c.base + "/v1/" + kind
	if ns != "" {
		u += "?namespace=" + url.QueryEscape(ns)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return httpError(resp)
	}
	var lr struct {
		Items    []json.RawMessage `json:"items"`
		Revision uint64            `json:"revision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return err
	}
	return c.renderList(kind, lr.Items)
}

// Delete removes an object (read-then-delete to supply the current revision).
func (c *Client) Delete(ctx context.Context, kind, ns, name string) error {
	kind = canonicalKind(kind)
	if ns == "" {
		ns = "default"
	}
	existing, ok, err := c.getObject(ctx, kind, ns, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s/%s not found", kind, name)
	}
	u := fmt.Sprintf("%s/v1/%s/%s/%s?rev=%d", c.base, kind, ns, name, existing.GetMeta().Revision)
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return httpError(resp)
	}
	fmt.Fprintf(c.out, "%s/%s deleted\n", kind, name)
	return nil
}

// Logs streams a container's logs. Wired when the kubelet/cri path exists.
func (c *Client) Logs(ctx context.Context, ns, pod string) error {
	return fmt.Errorf("logs: not implemented until the kubelet/cri path lands")
}

// Latency fetches and renders the RTT latency summary for a Service.
func (c *Client) Latency(ctx context.Context, ns, name string) error {
	if ns == "" {
		ns = "default"
	}
	u := fmt.Sprintf("%s/v1/latency/service/%s/%s", c.base, ns, name)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(c.out, "service/%s: no latency samples yet\n", name)
		return nil
	}
	if resp.StatusCode >= 300 {
		return httpError(resp)
	}
	var lat struct {
		P50Micros uint32 `json:"p50Micros"`
		P95Micros uint32 `json:"p95Micros"`
		Samples   uint64 `json:"samples"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lat); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "service/%s  p50=%s  p95=%s  samples=%d\n",
		name, micros(lat.P50Micros), micros(lat.P95Micros), lat.Samples)
	return nil
}

// micros renders microseconds as a short human string (us or ms).
func micros(us uint32) string {
	if us < 1000 {
		return fmt.Sprintf("%dµs", us)
	}
	return fmt.Sprintf("%.1fms", float64(us)/1000)
}

// --- internals -------------------------------------------------------------

// kindAliases maps lowercase user input (singular, plural, and common short
// forms) to the one canonical kind spelling the API and store use. neko-box
// has a closed, compiled-in set of kinds (no CRD discovery), so this is a tiny
// static table rather than k8s's RESTMapper/discovery machinery.
var kindAliases = map[string]string{
	"pod": "Pod", "pods": "Pod", "po": "Pod",
	"namespace": "Namespace", "namespaces": "Namespace", "ns": "Namespace",
	"deployment": "Deployment", "deployments": "Deployment", "deploy": "Deployment",
	"replicaset": "ReplicaSet", "replicasets": "ReplicaSet", "rs": "ReplicaSet",
	"service": "Service", "services": "Service", "svc": "Service",
	"endpointslice": "EndpointSlice", "endpointslices": "EndpointSlice", "eps": "EndpointSlice",
}

// canonicalKind resolves user-typed kind input to the canonical spelling. An
// exact canonical kind passes through unchanged; an unknown input is returned
// as-is so the server produces the authoritative "unknown kind" error.
func canonicalKind(kind string) string {
	if c, ok := kindAliases[strings.ToLower(kind)]; ok {
		return c
	}
	return kind
}

// getObject GETs one object and decodes it into its concrete api type.
func (c *Client) getObject(ctx context.Context, kind, ns, name string) (api.Object, bool, error) {
	u := fmt.Sprintf("%s/v1/%s/%s/%s", c.base, kind, ns, name)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode >= 300 {
		return nil, false, httpError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	obj, err := api.New(kind)
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(raw, obj); err != nil {
		return nil, false, err
	}
	return obj, true, nil
}

// renderList prints a table whose columns depend on the kind: Pods show
// phase/IP/ready, Services show the cluster IP, everything else falls back to a
// generic name/revision table.
func (c *Client) renderList(kind string, items []json.RawMessage) error {
	tw := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
	defer tw.Flush()

	decode := func(raw json.RawMessage) (api.Object, error) {
		obj, err := api.New(kind)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, obj); err != nil {
			return nil, err
		}
		return obj, nil
	}

	switch kind {
	case "Pod":
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tPHASE\tIP\tREADY")
		for _, raw := range items {
			obj, err := decode(raw)
			if err != nil {
				return err
			}
			p := obj.(*api.Pod)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\n",
				p.Meta.Namespace, p.Meta.Name, dash(p.Status.Phase), dash(p.Status.PodIP), p.Status.Ready)
		}
	case "Service":
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tCLUSTERIP")
		for _, raw := range items {
			obj, err := decode(raw)
			if err != nil {
				return err
			}
			s := obj.(*api.Service)
			fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Meta.Namespace, s.Meta.Name, dash(s.Spec.ClusterIP))
		}
	default:
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tREVISION")
		for _, raw := range items {
			obj, err := decode(raw)
			if err != nil {
				return err
			}
			m := obj.GetMeta()
			fmt.Fprintf(tw, "%s\t%s\t%d\n", m.Namespace, m.Name, m.Revision)
		}
	}
	return nil
}

// dash renders an empty field as "-" so columns stay readable.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func httpError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	if body.Error != "" {
		return fmt.Errorf("server %d: %s", resp.StatusCode, body.Error)
	}
	return fmt.Errorf("server %d: %s", resp.StatusCode, string(raw))
}
