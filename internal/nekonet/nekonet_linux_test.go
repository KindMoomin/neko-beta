//go:build linux

package nekonet

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// TestDecodeRTT guards the rtt_event wire layout (bpf/rtt/rtt.h): 24 bytes,
// addresses network-order (A.B.C.D for AddrFrom4), ports host-order LE, srtt
// and stamp LE. If the C struct or decode drifts, this catches it.
func TestDecodeRTT(t *testing.T) {
	b := make([]byte, rttRecordSize)
	// saddr 10.244.0.1 (network order = bytes 10,244,0,1).
	copy(b[0:4], []byte{10, 244, 0, 1})
	// daddr 10.244.0.2.
	copy(b[4:8], []byte{10, 244, 0, 2})
	binary.LittleEndian.PutUint16(b[8:10], 54321) // sport
	binary.LittleEndian.PutUint16(b[10:12], 80)   // dport
	binary.LittleEndian.PutUint32(b[12:16], 1234) // srtt_us
	binary.LittleEndian.PutUint64(b[16:24], 999)  // stamp_ns

	s, ok := decodeRTT(b)
	if !ok {
		t.Fatal("decodeRTT returned !ok for a full record")
	}
	if s.SrcIP != netip.AddrFrom4([4]byte{10, 244, 0, 1}) {
		t.Errorf("SrcIP = %v, want 10.244.0.1", s.SrcIP)
	}
	if s.DstIP != netip.AddrFrom4([4]byte{10, 244, 0, 2}) {
		t.Errorf("DstIP = %v, want 10.244.0.2", s.DstIP)
	}
	if s.SrcPort != 54321 || s.DstPort != 80 {
		t.Errorf("ports = %d/%d, want 54321/80", s.SrcPort, s.DstPort)
	}
	if s.RTTMicros != 1234 {
		t.Errorf("RTTMicros = %d, want 1234", s.RTTMicros)
	}
	if s.StampNanos != 999 {
		t.Errorf("StampNanos = %d, want 999", s.StampNanos)
	}
}

func TestDecodeRTTShortRecord(t *testing.T) {
	if _, ok := decodeRTT(make([]byte, rttRecordSize-1)); ok {
		t.Error("decodeRTT accepted a short record")
	}
}
