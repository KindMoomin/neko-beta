// SPDX-License-Identifier: GPL-2.0
//
// neko-box RTT observer — a kprobe on tcp_rcv_established that reads the smoothed
// RTT off each TCP socket and emits it to a ring buffer. neko-box owns and loads
// this (internal/nekonet); it replaces the vendored neko-net sock_ops program,
// whose BPF_SOCK_OPS_RTT_CB callback the node kernel (linuxkit) never delivers.
// A kprobe on tcp_rcv_established fires on every data segment on an established
// connection, so srtt_us is always current.
//
// CO-RE: struct field offsets are read via BPF_CORE_READ against vmlinux.h and
// relocated against the running kernel's BTF at load time.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_endian.h>

#include "rtt.h"

#define AF_INET 2

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 256 * 1024); // 256 KiB
} RTT_EVENTS SEC(".maps");

// neko_rtt fires on tcp_rcv_established(struct sock *sk, struct sk_buff *skb).
// BPF_KPROBE unpacks the pt_regs so arg0 is the struct sock *.
SEC("kprobe/tcp_rcv_established")
int BPF_KPROBE(neko_rtt, struct sock *sk)
{
	// IPv4 only (matches the v4 rtt_event / Go netip.AddrFrom4 decode).
	__u16 family = BPF_CORE_READ(sk, __sk_common.skc_family);
	if (family != AF_INET)
		return 0;

	struct tcp_sock *tp = (struct tcp_sock *)sk;
	// tcp_sock.srtt_us is stored as (srtt << 3); shift back to microseconds.
	__u32 srtt_us = BPF_CORE_READ(tp, srtt_us) >> 3;
	if (srtt_us == 0)
		return 0; // no RTT estimate yet — skip

	struct rtt_event *e = bpf_ringbuf_reserve(&RTT_EVENTS, sizeof(*e), 0);
	if (!e)
		return 0;

	// Addresses: keep NETWORK byte order (Go AddrFrom4 wants A.B.C.D bytes).
	e->saddr = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
	e->daddr = BPF_CORE_READ(sk, __sk_common.skc_daddr);
	// Ports: emit HOST order. skc_num is already host-order (local); skc_dport
	// is network-order (remote), so convert it. (The asymmetry is a known trap.)
	e->sport = BPF_CORE_READ(sk, __sk_common.skc_num);
	e->dport = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
	e->srtt_us = srtt_us;
	e->stamp_ns = bpf_ktime_get_ns();

	bpf_ringbuf_submit(e, 0);
	return 0;
}

// Required: kprobe + BPF_CORE_READ (bpf_probe_read_kernel) are GPL-gated; without
// a GPL-compatible license the verifier rejects the load.
char LICENSE[] SEC("license") = "GPL";
