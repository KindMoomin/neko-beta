// Package cri wraps containerd via the CRI gRPC API (k8s.io/cri-api v1) — the
// high-level runtime (decision D9). It pulls images and manages pod sandboxes
// and containers for the kubelet. On kindest/node the endpoint is
// unix:///run/containerd/containerd.sock.
//
// Concrete struct, not an interface (one impl; a fake lives next to a test if
// one is ever needed).
package cri

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimev1 "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/neko-box/neko/internal/api"
)

// Runtime is a CRI-gRPC client to the in-node containerd.
type Runtime struct {
	conn    *grpc.ClientConn
	rt      runtimev1.RuntimeServiceClient
	img     runtimev1.ImageServiceClient
	podCIDR string // informational; IPAM owns allocation
}

// New dials the containerd CRI endpoint (a unix socket path or unix:// URL).
func New(endpoint string) (*Runtime, error) {
	target := endpoint
	if len(endpoint) > 0 && endpoint[0] == '/' {
		target = "unix://" + endpoint
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial CRI %s: %w", target, err)
	}
	return &Runtime{
		conn: conn,
		rt:   runtimev1.NewRuntimeServiceClient(conn),
		img:  runtimev1.NewImageServiceClient(conn),
	}, nil
}

// Close releases the gRPC connection.
func (rt *Runtime) Close() error {
	if rt.conn != nil {
		return rt.conn.Close()
	}
	return nil
}

// Version verifies connectivity and returns the runtime version string.
func (rt *Runtime) Version(ctx context.Context) (string, error) {
	resp, err := rt.rt.Version(ctx, &runtimev1.VersionRequest{})
	if err != nil {
		return "", err
	}
	return resp.RuntimeName + " " + resp.RuntimeVersion, nil
}

// EnsureImage pulls ref if it is not already present.
func (rt *Runtime) EnsureImage(ctx context.Context, ref string) error {
	spec := &runtimev1.ImageSpec{Image: ref}
	status, err := rt.img.ImageStatus(ctx, &runtimev1.ImageStatusRequest{Image: spec})
	if err != nil {
		return fmt.Errorf("image status %s: %w", ref, err)
	}
	if status.Image != nil {
		return nil // already present
	}
	if _, err := rt.img.PullImage(ctx, &runtimev1.PullImageRequest{Image: spec}); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// Sandbox is a running pod sandbox: its CRI id plus the config it was created
// with (CreateContainer requires the same SandboxConfig).
type Sandbox struct {
	ID     string
	config *runtimev1.PodSandboxConfig
}

// CreateSandbox creates and starts the pod sandbox (which owns the pod's netns)
// and returns a Sandbox handle. netnsPath is informational for M1 (CNI sets up
// networking around the sandbox).
func (rt *Runtime) CreateSandbox(ctx context.Context, pod *api.Pod, netnsPath string) (*Sandbox, error) {
	cfg := &runtimev1.PodSandboxConfig{
		Metadata: &runtimev1.PodSandboxMetadata{
			Name:      pod.Meta.Name,
			Namespace: pod.Meta.Namespace,
			Uid:       pod.Meta.UID,
			Attempt:   0,
		},
		Labels: map[string]string{"neko.box/pod": pod.Meta.Name},
		Linux: &runtimev1.LinuxPodSandboxConfig{
			// kindest/node runs containerd with SystemdCgroup=true, so the
			// cgroup parent must be a systemd slice ("slice:prefix:name" form),
			// not a cgroupfs path — otherwise runc rejects it (see docs M4).
			CgroupParent: systemdSlice(pod),
			SecurityContext: &runtimev1.LinuxSandboxSecurityContext{
				// Pod-level namespace options. Without an explicit NamespaceOption
				// the pause task can fail to establish shareable namespaces in a
				// nested containerd, leaving later containers unable to join it.
				NamespaceOptions: &runtimev1.NamespaceOption{
					Network: runtimev1.NamespaceMode_POD,
					Pid:     runtimev1.NamespaceMode_CONTAINER,
					Ipc:     runtimev1.NamespaceMode_POD,
				},
			},
		},
	}
	resp, err := rt.rt.RunPodSandbox(ctx, &runtimev1.RunPodSandboxRequest{Config: cfg})
	if err != nil {
		return nil, fmt.Errorf("run sandbox %s: %w", pod.Meta.Name, err)
	}
	return &Sandbox{ID: resp.PodSandboxId, config: cfg}, nil
}

// systemdSlice builds a systemd cgroup slice name for a pod. CRI expects the
// ".slice" suffixed systemd form; containerd translates it to the
// "slice:prefix:name" triple runc wants under SystemdCgroup=true.
func systemdSlice(pod *api.Pod) string {
	return "neko-" + pod.Meta.Namespace + ".slice"
}

// StartContainer creates and starts one container in the sandbox and returns its
// ID. It reuses the sandbox's own config, as CreateContainer requires.
func (rt *Runtime) StartContainer(ctx context.Context, sb *Sandbox, c api.Container) (string, error) {
	ctrCfg := &runtimev1.ContainerConfig{
		Metadata: &runtimev1.ContainerMetadata{Name: c.Name},
		Image:    &runtimev1.ImageSpec{Image: c.Image},
		// A long-running command so the container doesn't exit immediately.
		// (Real pods carry their own entrypoint; M1 containers have none yet.)
		Command: []string{"/bin/sh", "-c", "sleep 3600"},
	}
	created, err := rt.rt.CreateContainer(ctx, &runtimev1.CreateContainerRequest{
		PodSandboxId:  sb.ID,
		Config:        ctrCfg,
		SandboxConfig: sb.config,
	})
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", c.Name, err)
	}
	if _, err := rt.rt.StartContainer(ctx, &runtimev1.StartContainerRequest{ContainerId: created.ContainerId}); err != nil {
		return "", fmt.Errorf("start container %s: %w", c.Name, err)
	}
	return created.ContainerId, nil
}

// Stop stops and removes the sandbox (and, transitively, its containers).
func (rt *Runtime) Stop(ctx context.Context, sandboxID string) error {
	if _, err := rt.rt.StopPodSandbox(ctx, &runtimev1.StopPodSandboxRequest{PodSandboxId: sandboxID}); err != nil {
		return fmt.Errorf("stop sandbox %s: %w", sandboxID, err)
	}
	if _, err := rt.rt.RemovePodSandbox(ctx, &runtimev1.RemovePodSandboxRequest{PodSandboxId: sandboxID}); err != nil {
		return fmt.Errorf("remove sandbox %s: %w", sandboxID, err)
	}
	return nil
}

// SandboxIP returns the CNI-assigned IP of a sandbox (containerd runs the CNI
// plugins at sandbox creation; the IP shows up in the sandbox status).
func (rt *Runtime) SandboxIP(ctx context.Context, sandboxID string) (string, error) {
	resp, err := rt.rt.PodSandboxStatus(ctx, &runtimev1.PodSandboxStatusRequest{PodSandboxId: sandboxID})
	if err != nil {
		return "", fmt.Errorf("sandbox status %s: %w", sandboxID, err)
	}
	if net := resp.GetStatus().GetNetwork(); net != nil {
		return net.Ip, nil
	}
	return "", nil
}
