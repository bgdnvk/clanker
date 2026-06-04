package linear

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bgdnvk/clanker/internal/secfile"
)

// ConversationEntry is a single Q&A turn against the Linear ask agent.
type ConversationEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Question    string    `json:"question"`
	Answer      string    `json:"answer"`
	WorkspaceID string    `json:"workspace_id"`
}

// ConversationHistory persists Linear ask sessions per-workspace under
// ~/.clanker/linear-{workspaceID}.json — same pattern as Sentry's history.
type ConversationHistory struct {
	Entries     []ConversationEntry `json:"entries"`
	WorkspaceID string              `json:"workspace_id"`
	LastStatus  *AccountStatus      `json:"last_status,omitempty"`
	mu          sync.RWMutex
}

const (
	MaxHistoryEntries        = 20
	MaxAnswerLengthInContext = 500
)

func NewConversationHistory(workspaceID string) *ConversationHistory {
	return &ConversationHistory{
		Entries:     make([]ConversationEntry, 0),
		WorkspaceID: workspaceID,
	}
}

func (h *ConversationHistory) AddEntry(question, answer, workspaceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Entries = append(h.Entries, ConversationEntry{
		Timestamp:   time.Now(),
		Question:    question,
		Answer:      answer,
		WorkspaceID: workspaceID,
	})
	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
}

func (h *ConversationHistory) UpdateAccountStatus(status *AccountStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.LastStatus = status
}

func (h *ConversationHistory) GetRecentContext(maxEntries int) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.Entries) == 0 {
		return ""
	}
	start := 0
	if len(h.Entries) > maxEntries {
		start = len(h.Entries) - maxEntries
	}
	var sb strings.Builder
	for _, e := range h.Entries[start:] {
		sb.WriteString("Q: ")
		sb.WriteString(e.Question)
		sb.WriteString("\nA: ")
		ans := e.Answer
		if len(ans) > MaxAnswerLengthInContext {
			ans = ans[:MaxAnswerLengthInContext] + "..."
		}
		sb.WriteString(ans)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func (h *ConversationHistory) GetAccountStatusContext() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.LastStatus == nil {
		return ""
	}
	return fmt.Sprintf(
		"Workspace: %s — Teams: %d — In-progress issues: %d — Active projects: %d (snapshot at %s)",
		h.LastStatus.WorkspaceName,
		h.LastStatus.TeamCount,
		h.LastStatus.StartedIssueCount,
		h.LastStatus.ActiveProjectCount,
		h.LastStatus.Timestamp.Format(time.RFC3339),
	)
}

// safeSlug strips anything outside [A-Za-z0-9_-] so a malicious workspaceID
// (e.g. "../../etc/passwd") can't escape the ~/.clanker directory when
// filepath.Join resolves the path. Linear workspace IDs are UUIDs so this
// is paranoia for the env-var/header case where an operator could pass
// an arbitrary string.
func safeSlug(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_':
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}

func historyPath(workspaceID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".clanker")
	if err := secfile.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("linear-%s.json", safeSlug(workspaceID))), nil
}

func (h *ConversationHistory) Load() error {
	path, err := historyPath(h.WorkspaceID)
	if err != nil {
		return err
	}
	data, err := secfile.ReadPrivate(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return json.Unmarshal(data, h)
}

func (h *ConversationHistory) Save() error {
	path, err := historyPath(h.WorkspaceID)
	if err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return secfile.WritePrivate(path, data)
}
