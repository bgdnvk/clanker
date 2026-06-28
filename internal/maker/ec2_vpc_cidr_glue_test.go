package maker

import (
	"net"
	"testing"
)

func TestCIDRRangeHandlesFullIPv4Space(t *testing.T) {
	start, end, ok := cidrRange("0.0.0.0/0")
	if !ok {
		t.Fatal("expected /0 to parse")
	}
	if start != 0 || end != ^uint32(0) {
		t.Fatalf("range = %d-%d, want 0-%d", start, end, ^uint32(0))
	}
}

func TestUint32ToIPv4(t *testing.T) {
	if got := uint32ToIPv4(0xC0A8010A); !got.Equal(net.ParseIP("192.168.1.10")) {
		t.Fatalf("ip = %s, want 192.168.1.10", got)
	}
}
