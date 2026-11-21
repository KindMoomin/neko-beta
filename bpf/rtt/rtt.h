#ifndef NEKO_RTT_H
#define NEKO_RTT_H

/* rtt_event is neko-box's on-wire RTT record, written to the RTT_EVENTS ring
 * buffer by bpf/rtt/rtt.bpf.c and decoded by internal/nekonet (decodeRTT).
 *
 * 24 bytes, naturally aligned, no padding holes. Both producer (BPF on the
 * arm64 LE node) and consumer (Go on the same node) are little-endian.
 *
 * Byte order (read the comments — this is the classic trap):
 *   saddr/daddr : IPv4 in NETWORK byte order (so Go's netip.AddrFrom4 gets the
 *                 bytes already in A.B.C.D order).
 *   sport/dport : HOST byte order. The kernel stores skc_num (local) in host
 *                 order but skc_dport (remote) in network order, so the BPF
 *                 program converts dport with bpf_ntohs before emitting; both
 *                 ports are therefore host-order on the wire.
 */
struct rtt_event {
	__u32 saddr;    /* local  IPv4, network byte order */
	__u32 daddr;    /* remote IPv4, network byte order */
	__u16 sport;    /* local  port, host byte order    */
	__u16 dport;    /* remote port, host byte order     */
	__u32 srtt_us;  /* smoothed RTT in microseconds (tcp_sock.srtt_us >> 3) */
	__u64 stamp_ns; /* bpf_ktime_get_ns() at the sample */
};

#endif /* NEKO_RTT_H */
