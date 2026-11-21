//go:build linux

package nekonet

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// On-wire record emitted by neko-box's kprobe program (bpf/rtt/rtt.h's
// rtt_event): 24 bytes, little-endian. saddr/daddr are IPv4 in network byte
// order; sport/dport are host order (the C side converts dport). srtt_us is
// already microseconds (tcp_sock.srtt_us >> 3); stamp_ns is bpf_ktime_get_ns().
const (
	rttRecordSize = 24
	kprobeSymbol  = "tcp_rcv_established" // the kernel fn we attach to
	progSymbol    = "neko_rtt"            // our program (see bpf/rtt/rtt.bpf.c)
	ringbufMap    = "RTT_EVENTS"          // the ring buffer map
)

// Dataplane owns the loaded BPF collection, the kprobe attachment, and the RTT
// ring reader.
type Dataplane struct {
	coll   *ebpf.Collection
	lnk    link.Link
	reader *ringbuf.Reader
}

// Load loads the baked RTT object, attaches its program via a kprobe on
// tcp_rcv_established (fires on every established-connection data segment, so
// srtt_us is always current), and opens the ring buffer reader.
func (d *Dataplane) Load(ctx context.Context) error {
	spec, err := ebpf.LoadCollectionSpec(bpfObjectPath)
	if err != nil {
		return fmt.Errorf("load BPF spec %s: %w", bpfObjectPath, err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return fmt.Errorf("new BPF collection: %w", err)
	}
	prog := coll.Programs[progSymbol]
	if prog == nil {
		coll.Close()
		return fmt.Errorf("program %q not found in %s", progSymbol, bpfObjectPath)
	}
	rbMap := coll.Maps[ringbufMap]
	if rbMap == nil {
		coll.Close()
		return fmt.Errorf("ringbuf map %q not found in %s", ringbufMap, bpfObjectPath)
	}

	lnk, err := link.Kprobe(kprobeSymbol, prog, nil)
	if err != nil {
		coll.Close()
		return fmt.Errorf("attach kprobe %s: %w", kprobeSymbol, err)
	}

	reader, err := ringbuf.NewReader(rbMap)
	if err != nil {
		lnk.Close()
		coll.Close()
		return fmt.Errorf("open RTT ringbuf reader: %w", err)
	}

	d.coll, d.lnk, d.reader = coll, lnk, reader
	return nil
}

// ReadRTT drains the RTT ring buffer, decoding each 12-byte RttEvent into a
// Sample, until ctx is cancelled or the reader is closed.
func (d *Dataplane) ReadRTT(ctx context.Context, out chan<- Sample) error {
	if d.reader == nil {
		return errors.New("nekonet: ReadRTT before Load")
	}
	// Unblock Read on ctx cancellation.
	go func() {
		<-ctx.Done()
		d.reader.Close()
	}()
	for {
		rec, err := d.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read RTT ringbuf: %w", err)
		}
		if len(rec.RawSample) < rttRecordSize {
			continue // short/garbled record; skip
		}
		s, ok := decodeRTT(rec.RawSample)
		if !ok {
			continue
		}
		select {
		case out <- s:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// decodeRTT turns a 24-byte rtt_event (bpf/rtt/rtt.h) into a Sample.
//   saddr[0:4], daddr[4:8]  IPv4, network byte order -> netip.AddrFrom4 (A.B.C.D).
//   sport[8:10], dport[10:12] host order (C side converted dport) -> LE uint16.
//   srtt_us[12:16]           microseconds. stamp_ns[16:24] mono nanoseconds.
func decodeRTT(b []byte) (Sample, bool) {
	if len(b) < rttRecordSize {
		return Sample{}, false
	}
	var saddr, daddr [4]byte
	copy(saddr[:], b[0:4])
	copy(daddr[:], b[4:8])
	return Sample{
		SrcIP:      netip.AddrFrom4(saddr),
		DstIP:      netip.AddrFrom4(daddr),
		SrcPort:    binary.LittleEndian.Uint16(b[8:10]),
		DstPort:    binary.LittleEndian.Uint16(b[10:12]),
		RTTMicros:  binary.LittleEndian.Uint32(b[12:16]),
		StampNanos: binary.LittleEndian.Uint64(b[16:24]),
	}, true
}

// Close detaches the program and releases the ring reader + collection.
func (d *Dataplane) Close() error {
	var errs []error
	if d.reader != nil {
		if err := d.reader.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if d.lnk != nil {
		if err := d.lnk.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if d.coll != nil {
		d.coll.Close()
	}
	return errors.Join(errs...)
}
