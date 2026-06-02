package linear

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

	h := NewConversationHistory("ws-abc")
	h.AddEntry("what's broken?", "the auth service", "ws-abc")
	h.AddEntry("priority?", "high", "ws-abc")
	h.UpdateAccountStatus(&AccountStatus{
		Timestamp:          time.Now(),
		WorkspaceID:        "ws-abc",
		WorkspaceName:      "Acme",
		TeamCount:          3,
		StartedIssueCount:  17,
		ActiveProjectCount: 4,
	})
	if err := h.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(tmpHome, ".clanker", "linear-ws-abc.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected history file at %s: %v", path, err)
	}

	loaded := NewConversationHistory("ws-abc")
	if err := loaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.LastStatus == nil || loaded.LastStatus.StartedIssueCount != 17 {
		t.Errorf("status not round-tripped: %+v", loaded.LastStatus)
	}
}

func TestSafeSlug_BlocksPathTraversal(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"acme", "acme"},
		{"my-workspace_42", "my-workspace_42"},
		{"../../etc/passwd", "etcpasswd"},
		{"/absolute/path", "absolutepath"},
		{"", "default"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := safeSlug(c.in); got != c.want {
				t.Errorf("safeSlug(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestConversationHistory_TruncateAnswer(t *testing.T) {
	h := NewConversationHistory("ws-abc")
	long := strings.Repeat("x", MaxAnswerLengthInContext*2)
	h.AddEntry("q", long, "ws-abc")
	ctx := h.GetRecentContext(5)
	if !strings.Contains(ctx, "...") {
		t.Errorf("expected truncation marker in context")
	}
}
