// Package clientcmd is the nekoctl client: it talks to the node agent's httpapi
// over HTTP, decoding neko-native TOML locally and rendering get/describe/logs.
// This is the only StoreAccess consumer that is always remote.
//
// Skeleton (architect Phase D).
package clientcmd

import "context"

// Client is nekoctl's HTTP client to a neko-box node agent.
type Client struct {
	// base URL (the published node-container port) + http.Client — elided.
}

// New builds a client for the node agent at addr (e.g. "127.0.0.1:7443").
func New(addr string) *Client {
	panic("not implemented")
}

// Apply decodes a neko-native TOML file and applies the object(s).
func (c *Client) Apply(ctx context.Context, tomlPath string) error {
	panic("not implemented")
}

// Get fetches and renders objects of a kind.
func (c *Client) Get(ctx context.Context, kind, ns string) error {
	panic("not implemented")
}

// Delete removes an object.
func (c *Client) Delete(ctx context.Context, kind, ns, name string) error {
	panic("not implemented")
}

// Logs streams a container's logs.
func (c *Client) Logs(ctx context.Context, ns, pod string) error {
	panic("not implemented")
}
