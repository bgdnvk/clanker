package railway

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

// ConversationHistory maintains conversation state for Railway ask mode.
type ConversationHistory struct {
	Entries     []ConversationEntry `json:"entries"`
	WorkspaceID string              `json:"workspace_id"`
	mu          sync.Mutex
}

// MaxHistoryEntries limits the conversation history size.
const MaxHistoryEntries = 20

// MaxAnswerLengthInContext limits how much of previous answers to include in context.
const MaxAnswerLengthInContext = 500

// NewConversationHistory creates a new conversation history for a workspace
// (or personal account).
func NewConversationHistory(workspaceID string) *ConversationHistory {
	return &ConversationHistory{
		Entries:     make([]ConversationEntry, 0),
		WorkspaceID: workspaceID,
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
	h.mu.Lock()
	defer h.mu.Unlock()

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

// Save persists the conversation history to disk using atomic write
// (temp file + rename). The mutex is only held while snapshotting and
// marshalling state — disk I/O happens with the lock released so concurrent
// callers aren't blocked on filesystem latency.
func (h *ConversationHistory) Save() error {
	h.mu.Lock()
	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
	snapshot := struct {
		Entries     []ConversationEntry `json:"entries"`
		WorkspaceID string              `json:"workspace_id"`
	}{
		Entries:     append([]ConversationEntry(nil), h.Entries...),
		WorkspaceID: h.WorkspaceID,
	}
	workspaceID := h.WorkspaceID
	h.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conversation history: %w", err)
	}

	dir, err := conversationDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create conversation directory: %w", err)
	}

	filename := filepath.Join(dir, fmt.Sprintf("railway_%s.json", sanitizeID(workspaceID)))

	// Atomic write: temp + rename.
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
		Entries     []ConversationEntry `json:"entries"`
		WorkspaceID string              `json:"workspace_id"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to parse conversation history: %w", err)
	}

	h.Entries = loaded.Entries
	h.WorkspaceID = loaded.WorkspaceID

	return nil
}

// filePath returns the on-disk path for this history file.
func (h *ConversationHistory) filePath() (string, error) {
	dir, err := conversationDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("railway_%s.json", sanitizeID(h.WorkspaceID))), nil
}

// conversationDir returns ~/.clanker/conversations.
func conversationDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".clanker", "conversations"), nil
}

// sanitizeID replaces characters that are invalid in filenames. An empty
// input yields the default "personal" bucket so callers never produce a
// filename like "railway_.json".
func sanitizeID(s string) string {
	if s == "" {
		return "personal"
	}
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

// truncateAnswer truncates text to maxLen characters, adding an ellipsis
// when truncated.
func truncateAnswer(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
