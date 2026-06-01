package sentry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConversationEntry is a single Q&A turn against the Sentry ask agent.
type ConversationEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	OrgSlug   string    `json:"org_slug"`
}

// ConversationHistory persists Sentry ask sessions per-org under
// ~/.clanker/sentry-{orgSlug}.json — same pattern as the Cloudflare history.
type ConversationHistory struct {
	Entries    []ConversationEntry `json:"entries"`
	OrgSlug    string              `json:"org_slug"`
	LastStatus *AccountStatus      `json:"last_status,omitempty"`
	mu         sync.RWMutex
}

const (
	MaxHistoryEntries        = 20
	MaxAnswerLengthInContext = 500
)

func NewConversationHistory(orgSlug string) *ConversationHistory {
	return &ConversationHistory{
		Entries: make([]ConversationEntry, 0),
		OrgSlug: orgSlug,
	}
}

func (h *ConversationHistory) AddEntry(question, answer, orgSlug string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Entries = append(h.Entries, ConversationEntry{
		Timestamp: time.Now(),
		Question:  question,
		Answer:    answer,
		OrgSlug:   orgSlug,
	})
	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
}

// UpdateAccountStatus stashes the latest snapshot so follow-up questions can
// reference orientation context (project count, unresolved count) without
// re-fetching.
func (h *ConversationHistory) UpdateAccountStatus(status *AccountStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.LastStatus = status
}

// GetRecentContext renders the last maxEntries turns as a single string
// suitable for prepending to an LLM prompt. Answers are truncated so a
// single long response can't crowd out the user's actual question.
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
		"Org: %s — Projects: %d — Unresolved issues: %d — Errors in last 24h: %d (snapshot at %s)",
		h.LastStatus.OrganizationSlug,
		h.LastStatus.ProjectCount,
		h.LastStatus.UnresolvedCount,
		h.LastStatus.ErrorCount24h,
		h.LastStatus.Timestamp.Format(time.RFC3339),
	)
}

func historyPath(orgSlug string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".clanker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	slug := orgSlug
	if slug == "" {
		slug = "default"
	}
	return filepath.Join(dir, fmt.Sprintf("sentry-%s.json", slug)), nil
}

func (h *ConversationHistory) Load() error {
	path, err := historyPath(h.OrgSlug)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
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
	path, err := historyPath(h.OrgSlug)
	if err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
