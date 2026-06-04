package flyio

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewConversationHistoryDefaultsToPersonal(t *testing.T) {
	h := NewConversationHistory("")
	if h.OrgSlug != "personal" {
		t.Errorf("OrgSlug = %q, want personal", h.OrgSlug)
	}
}

func TestAddEntryPrunes(t *testing.T) {
	h := NewConversationHistory("acme")
	// Push beyond the cap so the oldest entries are evicted.
	for i := 0; i < MaxHistoryEntries+5; i++ {
		h.AddEntry("q", "a")
	}
	if len(h.Entries) != MaxHistoryEntries {
		t.Fatalf("entries = %d, want %d", len(h.Entries), MaxHistoryEntries)
	}
}

func TestGetRecentContextEmpty(t *testing.T) {
	h := NewConversationHistory("acme")
	if got := h.GetRecentContext(5); got != "" {
		t.Errorf("empty history context = %q, want empty", got)
	}
}

func TestGetRecentContextFormatting(t *testing.T) {
	h := NewConversationHistory("acme")
	h.AddEntry("what apps?", "you have 3 apps")
	h.AddEntry("which region?", "iad")
	got := h.GetRecentContext(5)
	if !strings.Contains(got, "Q: what apps?") || !strings.Contains(got, "A: you have 3 apps") {
		t.Errorf("missing first exchange in: %q", got)
	}
	if !strings.Contains(got, "Q: which region?") || !strings.Contains(got, "A: iad") {
		t.Errorf("missing second exchange in: %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	h := NewConversationHistory("acme")
	h.AddEntry("first question", "first answer")
	h.AddEntry("second question", "second answer")
	if err := h.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File should land at ~/.clanker/conversations/flyio_acme.json.
	want := filepath.Join(dir, ".clanker", "conversations", "flyio_acme.json")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("expected file %s: %v", want, err)
	}
	if runtime.GOOS != "windows" {
		// Saved files must not be world-readable — they contain raw
		// operator Q&A (account IDs, ARNs, policy fragments). Drift
		// guard for #22.
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("file mode = %04o, want 0600", mode)
		}
		convDir, err := os.Stat(filepath.Dir(want))
		if err != nil {
			t.Fatalf("stat conv dir: %v", err)
		}
		if mode := convDir.Mode().Perm(); mode != 0o700 {
			t.Errorf("conversations dir mode = %04o, want 0700", mode)
		}
	}

	loaded := NewConversationHistory("acme")
	if err := loaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Errorf("loaded entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.Entries[0].Question != "first question" {
		t.Errorf("first question = %q, want first question", loaded.Entries[0].Question)
	}
}

func TestTruncateAnswer(t *testing.T) {
	if got := truncateAnswer("hello", 100); got != "hello" {
		t.Errorf("short answer should pass through, got %q", got)
	}
	got := truncateAnswer("hello world this is long", 5)
	if got != "hello..." {
		t.Errorf("truncated answer = %q, want hello...", got)
	}
}
