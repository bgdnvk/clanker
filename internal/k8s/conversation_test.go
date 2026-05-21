package k8s

import (
	"testing"
)

func TestResolveMaxHistoryEntries_Default(t *testing.T) {
	t.Setenv("CLANKER_K8S_HISTORY_MAX", "")
	if got := resolveMaxHistoryEntries(); got != DefaultMaxHistoryEntries {
		t.Errorf("empty env → got %d, want %d", got, DefaultMaxHistoryEntries)
	}
}

func TestResolveMaxHistoryEntries_HonoursEnv(t *testing.T) {
	t.Setenv("CLANKER_K8S_HISTORY_MAX", "200")
	if got := resolveMaxHistoryEntries(); got != 200 {
		t.Errorf("env=200 → got %d, want 200", got)
	}
}

func TestResolveMaxHistoryEntries_RejectsInvalid(t *testing.T) {
	cases := []string{"not-a-number", "0", "-5", "  "}
	for _, raw := range cases {
		t.Setenv("CLANKER_K8S_HISTORY_MAX", raw)
		if got := resolveMaxHistoryEntries(); got != DefaultMaxHistoryEntries {
			t.Errorf("env=%q → got %d, want default %d (invalid input must fall back)", raw, got, DefaultMaxHistoryEntries)
		}
	}
}

func TestConversationHistory_TrimsToConfiguredCap(t *testing.T) {
	// Bring the cap right down so we don't have to push 20+ entries.
	t.Setenv("CLANKER_K8S_HISTORY_MAX", "3")

	h := NewConversationHistory("test-cluster")
	for i := 0; i < 10; i++ {
		h.AddEntry("q", "a", "test-cluster")
	}

	if got := len(h.Entries); got != 3 {
		t.Errorf("after 10 appends with cap=3, len=%d, want 3", got)
	}
}

func TestConversationHistory_DefaultCapStillApplies(t *testing.T) {
	t.Setenv("CLANKER_K8S_HISTORY_MAX", "")

	h := NewConversationHistory("test-cluster")
	// Push past the default cap.
	for i := 0; i < DefaultMaxHistoryEntries+5; i++ {
		h.AddEntry("q", "a", "test-cluster")
	}

	if got := len(h.Entries); got != DefaultMaxHistoryEntries {
		t.Errorf("after over-cap appends, len=%d, want %d", got, DefaultMaxHistoryEntries)
	}
}
