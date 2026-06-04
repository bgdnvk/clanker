package notion

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

const (
	MaxHistoryEntries        = 20
	MaxAnswerLengthInContext = 500
)

// ConversationEntry is a single Q&A turn against the Notion ask agent.
type ConversationEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	Question      string    `json:"question"`
	Answer        string    `json:"answer"`
	WorkspaceName string    `json:"workspace_name"`
}

// ConversationHistory persists Notion ask sessions per-workspace under
// ~/.clanker/notion-{safeSlug(workspace_name)}.json — same pattern as
// the Sentry history. Workspace names contain spaces and punctuation,
// so safeSlug enforces the [A-Za-z0-9_-] charset.
type ConversationHistory struct {
	Entries       []ConversationEntry `json:"entries"`
	WorkspaceName string              `json:"workspace_name"`
	LastStatus    *AccountStatus      `json:"last_status,omitempty"`
	mu            sync.RWMutex
}

func NewConversationHistory(workspaceName string) *ConversationHistory {
	return &ConversationHistory{
		Entries:       make([]ConversationEntry, 0),
		WorkspaceName: workspaceName,
	}
}

func (h *ConversationHistory) AddEntry(question, answer, workspaceName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Entries = append(h.Entries, ConversationEntry{
		Timestamp:     time.Now(),
		Question:      question,
		Answer:        answer,
		WorkspaceName: workspaceName,
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
		"Workspace: %s — Accessible pages: %d — Databases: %d (snapshot at %s)",
		h.LastStatus.WorkspaceName,
		h.LastStatus.AccessiblePages,
		h.LastStatus.DatabaseCount,
		h.LastStatus.Timestamp.Format(time.RFC3339),
	)
}

func historyPath(workspaceName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".clanker")
	if err := secfile.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("notion-%s.json", secfile.SafeSlug(workspaceName))), nil
}

func (h *ConversationHistory) Load() error {
	path, err := historyPath(h.WorkspaceName)
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
	path, err := historyPath(h.WorkspaceName)
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
