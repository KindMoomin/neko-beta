// Command neko is the single neko-box binary. The subcommand selects the role:
//
//	neko node-agent   run the in-container control plane + kubelet (the monolith)
//	neko up|down      boot/tear down the privileged node container (host side)
//	neko import       one-way import of k8s YAML / Helm charts (D3)
//	neko apply|get|delete|logs|latency|snapshot|restore
//	                  nekoctl verbs — thin HTTP clients to a running node-agent
//
// `nekoctl` is this same binary invoked as the client verbs (an alias/symlink).
//
// Skeleton (architect Phase D): dispatch is wired; role bodies are not implemented.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "node-agent":
		err = runNodeAgent(ctx, args)
	case "up":
		err = runUp(ctx, args)
	case "down":
		err = runDown(ctx, args)
	case "import", "apply", "get", "delete", "logs", "latency", "snapshot", "restore":
		err = runClient(ctx, cmd, args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "neko: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "neko %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `neko — a lightweight local Kubernetes-shaped runtime

usage:
  neko up|down                 manage the node container (host side)
  neko node-agent              run the in-container control plane (monolith)
  neko import <path>           import k8s YAML / Helm chart -> neko-native
  neko apply <file.toml>       apply neko-native objects
  neko get <kind> [ns]         list objects
  neko delete <kind> <ns> <name>
  neko logs <ns> <pod>
  neko latency <kind>/<name>
  neko snapshot|restore        declared-state cluster snapshot/restore
`)
}

// runNodeAgent boots the store+WAL, the httpapi seam, the controllers, the
// kubelet, the dataplane (BPF load + RTT), and DNS — all in one process.
func runNodeAgent(ctx context.Context, args []string) error {
	// Preflight (docs/PLATFORM.md M3): probe kernel/BTF/cgroup/bpffs; fail loudly.
	panic("not implemented")
}

// runUp boots the privileged node container (kindest/node), runs the preflight,
// and launches `neko node-agent` inside it.
func runUp(ctx context.Context, args []string) error {
	panic("not implemented")
}

// runDown stops and removes the node container.
func runDown(ctx context.Context, args []string) error {
	panic("not implemented")
}

// runClient dispatches the nekoctl verbs over HTTP to a running node-agent.
func runClient(ctx context.Context, verb string, args []string) error {
	panic("not implemented")
}
