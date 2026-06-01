package sentry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationHistory_RoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h := NewConversationHistory("acme")
	h.AddEntry("what's broken?", "DB is on fire", "acme")
	h.AddEntry("how bad?", "very", "acme")
	h.UpdateAccountStatus(&AccountStatus{
		Timestamp:        time.Now(),
		OrganizationSlug: "acme",
		ProjectCount:     5,
		UnresolvedCount:  12,
		ErrorCount24h:    300,
	})
	if err := h.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File must land in ~/.clanker/sentry-acme.json.
	path := filepath.Join(tmpHome, ".clanker", "sentry-acme.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected history file at %s: %v", path, err)
	}

	loaded := NewConversationHistory("acme")
	if err := loaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.LastStatus == nil || loaded.LastStatus.UnresolvedCount != 12 {
		t.Errorf("status not round-tripped: %+v", loaded.LastStatus)
	}
}

// TestConversationHistory_TruncateAnswer_InContext verifies that very long
// answers don't dominate the history context — important because the LLM
// prompt has limited space and the latest question should always lead.
func TestConversationHistory_TruncateAnswer_InContext(t *testing.T) {
	h := NewConversationHistory("acme")
	long := strings.Repeat("x", MaxAnswerLengthInContext*2)
	h.AddEntry("q", long, "acme")
	ctx := h.GetRecentContext(5)
	if !strings.Contains(ctx, "...") {
		t.Errorf("expected truncation marker in context")
	}
	if strings.Count(ctx, "x") > MaxAnswerLengthInContext+10 {
		t.Errorf("answer not truncated: len contains %d x's", strings.Count(ctx, "x"))
	}
}

// TestConversationHistory_TrimsOldEntries confirms the rolling cap on
// history entries — otherwise a long-running operator session would grow
// the JSON file indefinitely.
func TestConversationHistory_TrimsOldEntries(t *testing.T) {
	h := NewConversationHistory("acme")
	for i := range MaxHistoryEntries + 5 {
		h.AddEntry("q", "a", "acme")
		_ = i
	}
	if len(h.Entries) != MaxHistoryEntries {
		t.Errorf("entries = %d, want cap %d", len(h.Entries), MaxHistoryEntries)
	}
}

// TestSafeSlug_BlocksPathTraversal confirms the slug sanitiser strips path
// separators so a hostile org slug from an MCP caller can't escape the
// ~/.clanker directory. The bug existed because filepath.Join cleans `..`
// segments, so `../../etc/passwd` would resolve outside the intended dir.
func TestSafeSlug_BlocksPathTraversal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"acme", "acme"},
		{"my-org_42", "my-org_42"},
		{"../../etc/passwd", "etcpasswd"},
		{"/absolute/path", "absolutepath"},
		{"", "default"},
		{"...", "default"},
		{"acme/../etc", "acmeetc"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			got := safeSlug(c.in)
			if got != c.want {
				t.Errorf("safeSlug(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
