package notion

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConversationHistory_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	h := NewConversationHistory("My Workspace!!")
	h.AddEntry("what tables exist?", "Three databases: Incidents, Runbooks, OnCall.", "My Workspace!!")
	h.UpdateAccountStatus(&AccountStatus{
		Timestamp:       time.Now(),
		WorkspaceName:   "My Workspace!!",
		AccessiblePages: 7,
		DatabaseCount:   3,
	})
	if err := h.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// File should land under ~/.clanker/notion-MyWorkspace.json (safeSlug
	// strips spaces + punctuation).
	matches, _ := filepath.Glob(filepath.Join(tmp, ".clanker", "notion-*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected one history file, got %v", matches)
	}
	if got := filepath.Base(matches[0]); got != "notion-MyWorkspace.json" {
		t.Errorf("unexpected file name: %s", got)
	}

	// Reload and verify content.
	h2 := NewConversationHistory("My Workspace!!")
	if err := h2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(h2.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h2.Entries))
	}
	if h2.Entries[0].Question != "what tables exist?" {
		t.Errorf("question: %q", h2.Entries[0].Question)
	}
	if h2.LastStatus == nil || h2.LastStatus.AccessiblePages != 7 {
		t.Errorf("status not persisted: %+v", h2.LastStatus)
	}

	// File permissions should be 0600 (history may contain question text
	// against sensitive workspaces).
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode: got %o, want 600", mode)
	}
}

func TestSafeSlug_PathTraversalDefense(t *testing.T) {
	cases := map[string]string{
		"normal-workspace": "normal-workspace",
		"My Workspace":     "MyWorkspace",
		"../../etc/passwd": "etcpasswd",
		"with/slashes":     "withslashes",
		"!!!":              "default",
		"":                 "default",
		"abc123_DEF":       "abc123_DEF",
	}
	for in, want := range cases {
		if got := safeSlug(in); got != want {
			t.Errorf("safeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConversationHistory_TrimsToMaxEntries(t *testing.T) {
	h := NewConversationHistory("ws")
	for range MaxHistoryEntries + 5 {
		h.AddEntry("q", "a", "ws")
	}
	if len(h.Entries) != MaxHistoryEntries {
		t.Errorf("entries: got %d, want %d", len(h.Entries), MaxHistoryEntries)
	}
}

func TestGetRecentContext_TruncatesLongAnswer(t *testing.T) {
	h := NewConversationHistory("ws")
	long := make([]byte, MaxAnswerLengthInContext+200)
	for i := range long {
		long[i] = 'x'
	}
	h.AddEntry("q", string(long), "ws")
	got := h.GetRecentContext(5)
	if len(got) >= len(long) {
		t.Errorf("answer should have been truncated; output length %d (vs %d)", len(got), len(long))
	}
}
