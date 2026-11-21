// Package node owns the host-side lifecycle of the neko-box node container.
//
// The node is a privileged `kindest/node`-based container booted with a plain
// `docker run` — NO kind library, NO kubeadm. neko-box runs its own control
// plane (API/store/litterbox) inside it.
//
// The one non-obvious requirement is an **ext4-backed Docker volume mounted at
// `/var`**. Without it, `/var/lib/containerd` falls on the container's own
// overlayfs rootfs, and containerd's overlay snapshotter can't build a sandbox
// rootfs overlay on top of another overlay — the kernel returns EINVAL
// ("failed to mount rootfs component: invalid argument"). A local Docker volume
// is block-backed (ext4) on the Docker Desktop VM, which gives the snapshotter
// a real filesystem. This is the whole reason the old kind detour existed; see
// D11 in docs/DECISIONS.md (root-caused 2026-11-20).
package node

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/neko-box/neko/internal/cni"
)

// Config controls a neko-box node.
type Config struct {
	Name     string // base name; container is "<Name>-control-plane", volume "<Name>-var"
	Image    string // node image ref (default: the neko-owned image, built by `make node-image`)
	HostPort int    // host port to publish the node-agent API on
	PodCIDR  string // pod network CIDR
	NekoBin  string // optional: a linux/arm64 `neko` binary to overlay over the image's baked one
}

// Defaults fills unset fields.
func (c *Config) Defaults() {
	if c.Name == "" {
		c.Name = "neko"
	}
	if c.Image == "" {
		// The neko-owned node image: FROM kindest/node:v1.33.1 (containerd
		// v2.1.1) with the neko binary, CNI conflist, and eBPF runtime prep
		// baked in. Build it with `make node-image`.
		c.Image = "neko-box/node:v1.33.1"
	}
	if c.HostPort == 0 {
		c.HostPort = 7443
	}
	if c.PodCIDR == "" {
		c.PodCIDR = "10.244.0.0/16"
	}
}

// agentPort is the port the node-agent listens on inside the node.
const agentPort = 7443

// containerNameFor and volumeNameFor are the single source of truth for the
// names derived from a node's base name, so Up and Down always agree.
func containerNameFor(name string) string { return name + "-control-plane" }
func volumeNameFor(name string) string    { return name + "-var" }

// Up boots the node container via `docker run` (with the ext4 `/var` volume that
// makes containerd's snapshotter work nested), installs the CNI config, and
// launches `neko node-agent` inside it.
func Up(ctx context.Context, cfg Config) error {
	cfg.Defaults()
	name := containerNameFor(cfg.Name)
	vol := volumeNameFor(cfg.Name)

	if exists(ctx, name) {
		return fmt.Errorf("node %q already exists (use `neko down` first)", cfg.Name)
	}

	// The ext4-backed volume at /var — the fix for the overlay-on-overlay EINVAL
	// (see package doc / D11). A local Docker volume is block-backed, so
	// /var/lib/containerd no longer sits on the container's overlayfs rootfs.
	if err := dockerRun(ctx, "volume", "create", vol); err != nil {
		return fmt.Errorf("create /var volume: %w", err)
	}

	// Boot the node. These flags match kind's node container exactly (verified by
	// inspecting a working kind node), plus the --volume <vol>:/var fix.
	runArgs := []string{
		"run", "-d", "--name", name,
		"--privileged",
		"--cgroupns=private",
		"--security-opt", "seccomp=unconfined",
		"--security-opt", "apparmor=unconfined",
		"--security-opt", "label=disable",
		"--tmpfs", "/run",
		"--tmpfs", "/tmp",
		"--volume", "/lib/modules:/lib/modules:ro",
		"--volume", vol + ":/var",
		"--restart=on-failure:1",
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", cfg.HostPort, agentPort),
		cfg.Image,
	}
	if err := dockerRun(ctx, runArgs...); err != nil {
		return fmt.Errorf("run node container (is the image built? `make node-image`): %w", err)
	}

	// Wait for the node's systemd to bring containerd up before we touch it.
	if err := waitContainerd(ctx, name); err != nil {
		return fmt.Errorf("containerd did not come up in node %q: %w", cfg.Name, err)
	}

	if err := installCNI(ctx, name, cfg.PodCIDR); err != nil {
		return fmt.Errorf("install CNI config: %w", err)
	}
	// The image bakes a default `neko` binary; --neko-bin overlays a fresh build
	// over it (e.g. during development). Optional now that the image carries one.
	if cfg.NekoBin != "" {
		if err := copyNekoBinary(ctx, name, cfg.NekoBin); err != nil {
			return fmt.Errorf("copy neko binary: %w", err)
		}
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

// Down removes the node container and its /var volume.
func Down(ctx context.Context, name string) error {
	if name == "" {
		name = "neko"
	}
	container := containerNameFor(name)
	vol := volumeNameFor(name)

	if err := dockerRun(ctx, "rm", "-f", container); err != nil {
		return fmt.Errorf("remove node %q: %w", name, err)
	}
	// Remove the volume after the container is gone. Tolerate "not found" so
	// `down` is idempotent and survives a partial `up`.
	if err := dockerRun(ctx, "volume", "rm", vol); err != nil && !strings.Contains(err.Error(), "no such volume") {
		return fmt.Errorf("remove /var volume: %w", err)
	}
	fmt.Printf("node %q removed\n", name)
	return nil
}

// --- node-interior helpers (docker exec/cp into the node) -------------------

// exists reports whether a container with this name is already present.
func exists(ctx context.Context, name string) bool {
	return exec.CommandContext(ctx, "docker", "inspect", name).Run() == nil
}

// waitContainerd polls until the node's containerd CRI socket is up, or ctx /
// the deadline expires.
func waitContainerd(ctx context.Context, name string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := exec.CommandContext(ctx, "docker", "exec", name,
			"test", "-S", "/run/containerd/containerd.sock").Run(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after 60s waiting for /run/containerd/containerd.sock")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func installCNI(ctx context.Context, name, podCIDR string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", name,
		"sh", "-c", "mkdir -p /etc/cni/net.d && cat > "+cni.ConflistPath)
	cmd.Stdin = strings.NewReader(cni.Conflist(podCIDR))
	return cmd.Run()
}

func copyNekoBinary(ctx context.Context, name, binPath string) error {
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
