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
# Deferred (see docs/PLAN-ebpf-cni.md): baking the compiled RTT .o to
# /opt/neko/bpf/rtt.o and loading any BPF program. This image only PREPS the
# eBPF runtime (bpffs + pin dir); it does not yet ship or load a program.

# --- Stage A: build the linux/arm64 neko binary -----------------------------
FROM golang:1.26 AS build
WORKDIR /src
# Module graph first for layer caching, then the full source.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/neko ./cmd/neko

# --- Stage B: the node image ------------------------------------------------
FROM kindest/node:v1.33.1

# The baked default neko binary. `neko up --neko-bin` can overlay a fresh build.
COPY --from=build /out/neko /usr/local/bin/neko

# The default CNI conflist (ptp + host-local + loopback over the default pod
# CIDR). Mirrors internal/cni.Conflist("10.244.0.0/16") at internal/cni.ConflistPath.
# `neko up` re-installs this at runtime so a non-default --pod-cidr is honored;
# baking it means the common case needs no runtime write.
COPY images/node/10-neko.conflist /etc/cni/net.d/10-neko.conflist

# eBPF runtime prep: a boot-time systemd unit mounts bpffs and creates the pin
# dir. bpffs can only be mounted at runtime (not at image-build), hence a unit.
# Reserved baked path for the RTT object (filled by the eBPF phase):
#   /opt/neko/bpf/rtt.o
RUN mkdir -p /opt/neko/bpf
COPY images/node/neko-bpf-prep.service /etc/systemd/system/neko-bpf-prep.service
RUN systemctl enable neko-bpf-prep.service
