# rtt/ — vendored neko-net RTT program

NOT built in this skeleton. This directory is where the `sock_ops` RTT program
from the sibling `neko-net` project is vendored (as source to compile, or a
prebuilt `.o`) so `internal/nekonet` can load, attach, and drain it.

neko-box is the **sole loader and ring-buffer consumer** — neko-net's userspace
daemon does not run here (interrogation B2).

Source of truth today: `~/neko-net/neko-net-ebpf/` (aya, `sock_ops`, emits
`RttEvent{daddr,dport,srtt_us}` to a ring buffer). The richer per-pod payload
(src addr/port, timestamp) depends on neko-net's own M1.5 (risk M5b).

Vendoring mechanics are decided at the dataplane milestone.
