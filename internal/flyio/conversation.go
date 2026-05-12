package flyio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConversationEntry represents a single Q&A exchange.
type ConversationEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
}

// ConversationHistory maintains conversation state for Fly.io ask mode.
// Keyed on org slug — a token can see multiple orgs but each org gets its
// own history file. Personal-account tokens use the slug "personal".
type ConversationHistory struct {
	Entries []ConversationEntry `json:"entries"`
	OrgSlug string              `json:"org_slug"`
	mu      sync.RWMutex
}

// MaxHistoryEntries limits the conversation history size.
const MaxHistoryEntries = 20

// MaxAnswerLengthInContext limits how much of previous answers to include in context.
const MaxAnswerLengthInContext = 500

// NewConversationHistory creates a new conversation history for an org slug
// (or the literal string "personal" for non-org tokens).
func NewConversationHistory(orgSlug string) *ConversationHistory {
	if strings.TrimSpace(orgSlug) == "" {
		orgSlug = "personal"
	}
	return &ConversationHistory{
		Entries: make([]ConversationEntry, 0),
		OrgSlug: orgSlug,
	}
}

// AddEntry adds a new conversation entry and prunes old entries.
func (h *ConversationHistory) AddEntry(question, answer string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := ConversationEntry{
		Timestamp: time.Now(),
		Question:  question,
		Answer:    answer,
	}

	h.Entries = append(h.Entries, entry)

	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
}

// GetRecentContext returns recent conversation context as a formatted string
// for inclusion in LLM prompts.
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
	for i, entry := range h.Entries[start:] {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("Q: %s\n", entry.Question))
		sb.WriteString(fmt.Sprintf("A: %s\n", truncateAnswer(entry.Answer, MaxAnswerLengthInContext)))
	}

	return sb.String()
}

// Save persists the conversation history to disk using atomic write (temp + rename).
func (h *ConversationHistory) Save() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	dir, err := conversationDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create conversation directory: %w", err)
	}

	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conversation history: %w", err)
	}

	filename, err := h.filePath()
	if err != nil {
		return err
	}

	tmp := filename + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp conversation file: %w", err)
	}
	if err := os.Rename(tmp, filename); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to rename conversation file: %w", err)
	}

	return nil
}

// Load loads conversation history from disk.
func (h *ConversationHistory) Load() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	path, err := h.filePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read conversation file: %w", err)
	}

	var loaded struct {
		Entries []ConversationEntry `json:"entries"`
		OrgSlug string              `json:"org_slug"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to parse conversation history: %w", err)
	}

	h.Entries = loaded.Entries
	if strings.TrimSpace(loaded.OrgSlug) != "" {
		h.OrgSlug = loaded.OrgSlug
	}

	return nil
}

// filePath returns the on-disk path for this history file.
func (h *ConversationHistory) filePath() (string, error) {
	dir, err := conversationDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("flyio_%s.json", sanitizeID(h.OrgSlug))), nil
}

// conversationDir returns ~/.clanker/conversations.
func conversationDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".clanker", "conversations"), nil
}

// sanitizeID replaces characters that are invalid in filenames.
func sanitizeID(s string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
	)
	return replacer.Replace(s)
}

// truncateAnswer truncates text to maxLen characters, adding ellipsis if truncated.
func truncateAnswer(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
