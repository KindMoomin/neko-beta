# neko-box node image.
#
# FROM kindest/node (containerd v2.1.1 + runc + nested-container mounts kind got
# right) with the neko binary, CNI conflist, and eBPF runtime prep baked in.
# neko-box runs its own control plane inside — NO kubeadm, NO kind library.
#
# Build (linux/arm64 only for now; this machine / Docker Desktop):
#   make node-image
# which runs:
#   docker buildx build --platform linux/arm64 -t neko-box/node:v1.33.1 \
#       -f images/node/Dockerfile .
#
# The RTT eBPF object is a neko-box-owned C kprobe on tcp_rcv_established
# (bpf/rtt/rtt.bpf.c), baked to /opt/neko/bpf/rtt.o. The Go loader
# (internal/nekonet) loads it at runtime and attaches it via kprobe. A systemd
# unit preps the eBPF runtime (bpffs + pin dir) at boot.

# --- Stage A: build the linux/arm64 neko binary -----------------------------
FROM golang:1.26 AS build
WORKDIR /src
# Module graph first for layer caching, then the full source.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/neko ./cmd/neko

# --- Stage B: build the RTT eBPF object (C, CO-RE) --------------------------
# clang compiles bpf/rtt/rtt.bpf.c for the bpf target against a committed
# vmlinux.h; CO-RE relocates field offsets against the node kernel's BTF at
# load time. libbpf-dev supplies bpf_helpers.h / bpf_core_read.h / bpf_tracing.h.
FROM debian:bookworm-slim AS bpf
RUN apt-get update && apt-get install -y --no-install-recommends \
      clang llvm libbpf-dev && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY bpf/ ./bpf/
RUN mkdir -p /out && clang -target bpf -D__TARGET_ARCH_arm64 -mcpu=v3 -g -O2 \
      -I bpf/include -I bpf/rtt \
      -c bpf/rtt/rtt.bpf.c -o /out/rtt.o

# --- Stage C: the node image ------------------------------------------------
FROM kindest/node:v1.33.1

# The baked default neko binary. `neko up --neko-bin` can overlay a fresh build.
COPY --from=build /out/neko /usr/local/bin/neko

# The default CNI conflist (ptp + host-local + loopback over the default pod
# CIDR). Mirrors internal/cni.Conflist("10.244.0.0/16") at internal/cni.ConflistPath.
# `neko up` re-installs this at runtime so a non-default --pod-cidr is honored;
# baking it means the common case needs no runtime write.
COPY images/node/10-neko.conflist /etc/cni/net.d/10-neko.conflist

# The RTT eBPF object (neko-box's C kprobe), loaded by internal/nekonet at
# runtime. Program symbol `neko_rtt`, ringbuf map `RTT_EVENTS`.
RUN mkdir -p /opt/neko/bpf
COPY --from=bpf /out/rtt.o /opt/neko/bpf/rtt.o

# eBPF runtime prep: a boot-time systemd unit mounts bpffs and creates the pin
# dir. bpffs can only be mounted at runtime (not at image-build), hence a unit.
COPY images/node/neko-bpf-prep.service /etc/systemd/system/neko-bpf-prep.service
RUN systemctl enable neko-bpf-prep.service
