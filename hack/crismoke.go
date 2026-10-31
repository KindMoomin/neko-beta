//go:build ignore

// Throwaway CRI smoke test — runs INSIDE a kindest/node container against the
// live containerd CRI socket. Not part of the build (build tag 'ignore').
// Exercises the internal/cri package end to end.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/cri"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rt, err := cri.New("/run/containerd/containerd.sock")
	must(err, "new")
	defer rt.Close()

	ver, err := rt.Version(ctx)
	must(err, "version")
	fmt.Println("runtime:", ver)

	const image = "docker.io/library/busybox:latest"
	fmt.Println("pulling", image)
	must(rt.EnsureImage(ctx, image), "ensure image")

	pod := &api.Pod{
		TypeMeta: api.TypeMeta{Kind: "Pod", APIVersion: api.APIVersion},
		Meta:     api.ObjectMeta{Name: "smoke", Namespace: "default", UID: "smoke-uid-1"},
		Spec:     api.PodSpec{Containers: []api.Container{{Name: "c", Image: image}}},
	}

	sb, err := rt.CreateSandbox(ctx, pod, "")
	must(err, "create sandbox")
	fmt.Println("sandbox:", sb.ID)

	cid, err := rt.StartContainer(ctx, sb, pod.Spec.Containers[0])
	must(err, "start container")
	fmt.Println("container:", cid)

	fmt.Println("stopping sandbox")
	must(rt.Stop(ctx, sb.ID), "stop")

	fmt.Println("CRI SMOKE OK")
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", what, err)
		os.Exit(1)
	}
}
