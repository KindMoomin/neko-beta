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
func (c *Client) Get(ctx context.Context, kind, ns string) error {
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

// --- internals -------------------------------------------------------------

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

// renderList prints a simple NAME/NAMESPACE/REVISION table.
func (c *Client) renderList(kind string, items []json.RawMessage) error {
	tw := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tNAME\tREVISION")
	for _, raw := range items {
		obj, err := api.New(kind)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, obj); err != nil {
			return err
		}
		m := obj.GetMeta()
		fmt.Fprintf(tw, "%s\t%s\t%d\n", m.Namespace, m.Name, m.Revision)
	}
	return tw.Flush()
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
