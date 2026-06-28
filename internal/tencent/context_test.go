package tencent

import "testing"

func TestSafeInt64FromUint64(t *testing.T) {
	const maxInt64AsUint64 = uint64(1<<63 - 1)
	if got := safeInt64FromUint64(42); got != 42 {
		t.Fatalf("safeInt64FromUint64(42) = %d, want 42", got)
	}
	if got := safeInt64FromUint64(maxInt64AsUint64 + 1); got != int64(maxInt64AsUint64) {
		t.Fatalf("safeInt64FromUint64 overflow = %d, want %d", got, int64(maxInt64AsUint64))
	}
}
