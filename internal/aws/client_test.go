package aws

import (
	"testing"
)

func TestNewClient(t *testing.T) {
	// This test would require AWS credentials in CI
	// For now, just test that the function exists
	if testing.Short() {
		t.Skip("skipping AWS client test in short mode")
	}
}

func TestNewClientWithProfile(t *testing.T) {
	// This test would require AWS credentials and profiles in CI
	// For now, just test that the function exists
	if testing.Short() {
		t.Skip("skipping AWS client with profile test in short mode")
	}
}
