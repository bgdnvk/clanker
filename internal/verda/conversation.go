package verda

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConversationEntry is a single Q&A exchange.
type ConversationEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
}

// ConversationHistory maintains conversation state for Verda ask mode.
type ConversationHistory struct {
	Entries []ConversationEntry `json:"entries"`
	ScopeID string              `json:"scope_id"`
	mu      sync.RWMutex
}

// MaxHistoryEntries limits the conversation history size.
const MaxHistoryEntries = 20

// MaxAnswerLengthInContext limits how much of previous answers to include in context.
const MaxAnswerLengthInContext = 500

// NewConversationHistory creates a new conversation history keyed by scope
// (project_id for team accounts, "personal" otherwise).
func NewConversationHistory(scopeID string) *ConversationHistory {
	if strings.TrimSpace(scopeID) == "" {
		scopeID = "personal"
	}
	return &ConversationHistory{
		Entries: make([]ConversationEntry, 0),
		ScopeID: scopeID,
	}
}

// AddEntry records a new question/answer pair and prunes the log.
func (h *ConversationHistory) AddEntry(question, answer string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Entries = append(h.Entries, ConversationEntry{
		Timestamp: time.Now(),
		Question:  question,
		Answer:    answer,
	})

	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
}

// GetRecentContext returns a compact string of recent exchanges for the LLM prompt.
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
		sb.WriteString(fmt.Sprintf("A: %s\n", truncate(entry.Answer, MaxAnswerLengthInContext)))
	}
	return sb.String()
}

// Save persists the conversation to ~/.clanker/conversations/verda_<scope>.json.
func (h *ConversationHistory) Save() error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	dir, err := conversationDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create conversation dir: %w", err)
	}

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("verda_%s.json", sanitize(h.ScopeID)))
	return os.WriteFile(path, data, 0o644)
}

// Load restores history from disk. Missing file is not an error.
func (h *ConversationHistory) Load() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	dir, err := conversationDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("verda_%s.json", sanitize(h.ScopeID)))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read conversation: %w", err)
	}

	var loaded struct {
		Entries []ConversationEntry `json:"entries"`
		ScopeID string              `json:"scope_id"`
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse conversation: %w", err)
	}

	h.Entries = loaded.Entries
	if loaded.ScopeID != "" {
		h.ScopeID = loaded.ScopeID
	}
	return nil
}

func conversationDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clanker", "conversations"), nil
}

func sanitize(s string) string {
	r := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_",
	)
	return r.Replace(s)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
