package railway

import (
	"strings"
	"testing"
)

func TestFormatRailwayLogEntries(t *testing.T) {
	got := formatRailwayLogEntries([]map[string]any{{
		"timestamp": "2026-06-18T09:00:00Z",
		"severity":  "error",
		"message":   "request failed",
	}}, 10)
	for _, want := range []string{"2026-06-18T09:00:00Z", "error", "request failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted logs missing %q: %s", want, got)
		}
	}
}
