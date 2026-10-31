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
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neko-box/neko/internal/access"
	"github.com/neko-box/neko/internal/api"
	"github.com/neko-box/neko/internal/clientcmd"
	"github.com/neko-box/neko/internal/cri"
	"github.com/neko-box/neko/internal/httpapi"
	"github.com/neko-box/neko/internal/kubelet"
	"github.com/neko-box/neko/internal/node"
	"github.com/neko-box/neko/internal/store"
)

// defaultAddr is where the node-agent serves and the client looks by default.
const defaultAddr = "127.0.0.1:7443"

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

// runNodeAgent boots the store+WAL, the httpapi seam, and (when a CRI endpoint
// is reachable) the kubelet + trivial placement — all in one process. The
// controllers, dataplane (BPF + RTT), and DNS join here as those layers land.
func runNodeAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node-agent", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "listen address for the API")
	walPath := fs.String("wal", "", "WAL file path (empty = in-memory, no durability)")
	nodeName := fs.String("node", "node0", "this node's name (pods bound here run here)")
	criEndpoint := fs.String("cri-endpoint", "/run/containerd/containerd.sock", "containerd CRI socket (empty disables the kubelet)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := store.New(*walPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	acc := &access.InProc{S: st}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Kubelet + placement, if a CRI endpoint is reachable. Optional so the agent
	// can serve the API-only on a host without containerd (e.g. for testing).
	if *criEndpoint != "" {
		rt, err := cri.New(*criEndpoint)
		if err == nil {
			if _, verr := rt.Version(ctx); verr == nil {
				kl := kubelet.New(*nodeName, acc, rt)
				go func() {
					if rerr := kl.Run(ctx); rerr != nil && ctx.Err() == nil {
						log.Printf("kubelet stopped: %v", rerr)
					}
				}()
				go runBinder(ctx, acc, *nodeName) // trivial placement (D5)
				log.Printf("kubelet running (node=%s, cri=%s)", *nodeName, *criEndpoint)
			} else {
				log.Printf("CRI unreachable (%v); serving API only", verr)
			}
		} else {
			log.Printf("CRI dial failed (%v); serving API only", err)
		}
	}

	srv := &http.Server{Addr: *addr, Handler: httpapi.New(st, nil).Routes()}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()

	fmt.Fprintf(os.Stderr, "neko node-agent listening on %s (wal=%q)\n", *addr, *walPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// runBinder is neko-box's trivial placement (the kube-scheduler replacement, D5):
// single node, so bind every unbound Pod to this node. Watches Pods and sets
// Spec.NodeName where empty, which is what makes the kubelet act on them.
func runBinder(ctx context.Context, acc *access.InProc, nodeName string) {
	ch, err := acc.Watch(ctx, "Pod", "", 0)
	if err != nil {
		log.Printf("binder: watch: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				if ch, err = acc.Watch(ctx, "Pod", "", 0); err != nil {
					return
				}
				continue
			}
			pod, ok := ev.Object.(*api.Pod)
			if !ok || pod.Spec.NodeName != "" || ev.Type == store.Deleted {
				continue
			}
			pod.Spec.NodeName = nodeName
			if _, err := acc.Apply(ctx, pod); err != nil && err != store.ErrConflict {
				log.Printf("binder: bind %s/%s: %v", pod.Meta.Namespace, pod.Meta.Name, err)
			}
		}
	}
}

// runUp boots the privileged node container (kindest/node) with kind's docker
// flags so its entrypoint does the nested-container setup, installs CNI, and
// launches `neko node-agent` inside it.
func runUp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	name := fs.String("name", "", "node cluster name")
	image := fs.String("image", "", "kindest/node image")
	port := fs.Int("port", 7443, "host port to publish the node-agent API on")
	nekoBin := fs.String("neko-bin", "", "path to a linux/arm64 neko binary to run in the node")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return node.Up(ctx, node.Config{
		Name:     *name,
		Image:    *image,
		HostPort: *port,
		NekoBin:  *nekoBin,
	})
}

// runDown stops and removes the node container.
func runDown(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	name := fs.String("name", "", "node container name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return node.Down(ctx, *name)
}

// runClient dispatches the nekoctl verbs over HTTP to a running node-agent.
// The agent address comes from --addr, else $NEKO_ADDR, else the default.
func runClient(ctx context.Context, verb string, args []string) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	addr := fs.String("addr", envOr("NEKO_ADDR", defaultAddr), "node-agent address")
	ns := fs.String("n", "", "namespace (default: all for get, 'default' for others)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	c := clientcmd.New(*addr)

	switch verb {
	case "apply":
		if len(rest) != 1 {
			return fmt.Errorf("usage: neko apply <file.toml>")
		}
		return c.Apply(ctx, rest[0])
	case "get":
		if len(rest) != 1 {
			return fmt.Errorf("usage: neko get <kind> [-n ns]")
		}
		return c.Get(ctx, rest[0], *ns)
	case "delete":
		if len(rest) != 2 {
			return fmt.Errorf("usage: neko delete <kind> <name> [-n ns]")
		}
		return c.Delete(ctx, rest[0], *ns, rest[1])
	case "logs":
		if len(rest) != 1 {
			return fmt.Errorf("usage: neko logs <pod> [-n ns]")
		}
		return c.Logs(ctx, *ns, rest[0])
	case "import", "latency", "snapshot", "restore":
		return fmt.Errorf("%s: not implemented yet (see docs/PROGRESS.md)", verb)
	default:
		return fmt.Errorf("unknown client verb %q", verb)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
