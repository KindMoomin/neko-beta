// Package node owns the host-side lifecycle of the neko-box node container.
//
// Node PROVISIONING is delegated to kind's Go library
// (sigs.k8s.io/kind/pkg/cluster) with CreateWithStopBeforeSettingUpKubernetes:
// kind creates and correctly provisions the kindest/node container (the
// containerd/overlay/cgroup/mount setup we could not reliably replicate by hand)
// but STOPS before kubeadm — so NO Kubernetes control plane is stood up. We then
// run neko-box's own control plane inside that node.
//
// kind is a node-provisioner here, not a Kubernetes distribution (decision:
// accept the kind dependency for M1; removing it for a self-contained bring-up
// is a tracked goal — see docs/PROGRESS.md).
package node

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"sigs.k8s.io/kind/pkg/cluster"

	"github.com/neko-box/neko/internal/cni"
)

// Config controls a neko-box node.
type Config struct {
	Name     string // kind cluster name (node container becomes "<name>-control-plane")
	Image    string // kindest/node image ref
	HostPort int    // host port to publish the node-agent API on
	PodCIDR  string // pod network CIDR
	NekoBin  string // path to a linux/arm64 `neko` binary to run inside the node
}

// Defaults fills unset fields.
func (c *Config) Defaults() {
	if c.Name == "" {
		c.Name = "neko"
	}
	if c.Image == "" {
		// v1.33.1 ships containerd v2.1.1; kind provisions it correctly nested.
		c.Image = "kindest/node:v1.33.1"
	}
	if c.HostPort == 0 {
		c.HostPort = 7443
	}
	if c.PodCIDR == "" {
		c.PodCIDR = "10.244.0.0/16"
	}
}

// containerName is kind's name for the single control-plane node.
func (c *Config) containerName() string { return c.Name + "-control-plane" }

// agentPort is the port the node-agent listens on inside the node.
const agentPort = 7443

// Up provisions the node via kind (stopping before kubeadm), installs the CNI
// config, copies the neko binary in, and launches `neko node-agent` inside it.
func Up(ctx context.Context, cfg Config) error {
	cfg.Defaults()

	provider := cluster.NewProvider(cluster.ProviderWithDocker())

	if existing, _ := provider.List(); slices.Contains(existing, cfg.Name) {
		return fmt.Errorf("node cluster %q already exists (use `neko down` first)", cfg.Name)
	}

	// kind config: one control-plane node, publishing the agent port to the host.
	raw := fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: %d
    hostPort: %d
    listenAddress: "127.0.0.1"
    protocol: TCP
`, agentPort, cfg.HostPort)

	if err := provider.Create(cfg.Name,
		cluster.CreateWithNodeImage(cfg.Image),
		cluster.CreateWithRawConfig([]byte(raw)),
		cluster.CreateWithStopBeforeSettingUpKubernetes(true),
	); err != nil {
		return fmt.Errorf("provision node via kind: %w", err)
	}

	name := cfg.containerName()
	if err := installCNI(ctx, name, cfg.PodCIDR); err != nil {
		return fmt.Errorf("install CNI config: %w", err)
	}
	if err := copyNekoBinary(ctx, name, cfg.NekoBin); err != nil {
		return fmt.Errorf("copy neko binary: %w", err)
	}
	if err := dockerExec(ctx, name, "mkdir", "-p", "/var/lib/neko"); err != nil {
		return fmt.Errorf("create node state dir: %w", err)
	}
	if err := dockerExecDetached(ctx, name,
		"/usr/local/bin/neko", "node-agent", "--addr", fmt.Sprintf("0.0.0.0:%d", agentPort), "--wal", "/var/lib/neko/wal"); err != nil {
		return fmt.Errorf("start node-agent: %w", err)
	}

	fmt.Printf("node %q up; API on 127.0.0.1:%d\n", cfg.Name, cfg.HostPort)
	return nil
}

// Down deletes the kind-provisioned node cluster.
func Down(ctx context.Context, name string) error {
	if name == "" {
		name = "neko"
	}
	provider := cluster.NewProvider(cluster.ProviderWithDocker())
	if err := provider.Delete(name, ""); err != nil {
		return fmt.Errorf("delete node %q: %w", name, err)
	}
	fmt.Printf("node %q removed\n", name)
	return nil
}

// --- node-interior helpers (docker exec/cp into the kind node) --------------

func installCNI(ctx context.Context, name, podCIDR string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", name,
		"sh", "-c", "mkdir -p /etc/cni/net.d && cat > "+cni.ConflistPath)
	cmd.Stdin = strings.NewReader(cni.Conflist(podCIDR))
	return cmd.Run()
}

func copyNekoBinary(ctx context.Context, name, binPath string) error {
	if binPath == "" {
		return fmt.Errorf("no neko binary to copy (set --neko-bin to a linux build)")
	}
	return dockerRun(ctx, "cp", binPath, name+":/usr/local/bin/neko")
}

func dockerExec(ctx context.Context, name string, argv ...string) error {
	return dockerRun(ctx, append([]string{"exec", name}, argv...)...)
}

func dockerExecDetached(ctx context.Context, name string, argv ...string) error {
	return dockerRun(ctx, append([]string{"exec", "--detach", name}, argv...)...)
}

func dockerRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
