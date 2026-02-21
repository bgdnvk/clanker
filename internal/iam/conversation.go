package iam

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConversationEntry represents a single Q&A exchange
type ConversationEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	AccountID string    `json:"account_id"`
}

// ConversationHistory maintains conversation state for IAM ask mode
type ConversationHistory struct {
	Entries       []ConversationEntry `json:"entries"`
	AccountID     string              `json:"account_id"`
	LastSummary   *AccountSummary     `json:"last_summary,omitempty"`
	mu            sync.RWMutex
}

// MaxHistoryEntries limits the conversation history size
const MaxHistoryEntries = 20

// MaxAnswerLengthInContext limits how much of previous answers to include in context
const MaxAnswerLengthInContext = 500

// NewConversationHistory creates a new conversation history for an account
func NewConversationHistory(accountID string) *ConversationHistory {
	return &ConversationHistory{
		Entries:   make([]ConversationEntry, 0),
		AccountID: accountID,
	}
}

// AddEntry adds a new conversation entry
func (h *ConversationHistory) AddEntry(question, answer, accountID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := ConversationEntry{
		Timestamp: time.Now(),
		Question:  question,
		Answer:    answer,
		AccountID: accountID,
	}

	h.Entries = append(h.Entries, entry)

	// Trim old entries to keep history manageable
	if len(h.Entries) > MaxHistoryEntries {
		h.Entries = h.Entries[len(h.Entries)-MaxHistoryEntries:]
	}
}

// GetRecentContext returns recent conversation context as a formatted string
// for inclusion in LLM prompts
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
		sb.WriteString(fmt.Sprintf("A: %s\n", truncateText(entry.Answer, MaxAnswerLengthInContext)))
	}

	return sb.String()
}

// UpdateAccountSummary updates the cached account summary
func (h *ConversationHistory) UpdateAccountSummary(summary *AccountSummary) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.LastSummary = summary
}

// GetAccountSummary returns the cached account summary
func (h *ConversationHistory) GetAccountSummary() *AccountSummary {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.LastSummary
}

// GetAccountSummaryContext returns a string representation of account summary
// suitable for inclusion in LLM prompts
func (h *ConversationHistory) GetAccountSummaryContext() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.LastSummary == nil {
		return "IAM Account Summary: Not yet gathered"
	}

	return fmt.Sprintf(`IAM Account Summary:
- Account ID: %s
- Total Roles: %d
- Total Policies (Customer Managed): %d
- Total Users: %d
- Total Groups: %d
- Instance Profiles: %d
- MFA Devices: %d`,
		h.AccountID,
		h.LastSummary.RoleCount,
		h.LastSummary.PolicyCount,
		h.LastSummary.UserCount,
		h.LastSummary.GroupCount,
		h.LastSummary.InstanceProfiles,
		h.LastSummary.MFADevices)
}

// Clear clears all conversation history
func (h *ConversationHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Entries = make([]ConversationEntry, 0)
	h.LastSummary = nil
}

// Save persists the conversation history to disk
func (h *ConversationHistory) Save() error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	dir, err := getConversationDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create conversation directory: %w", err)
	}

	filename := filepath.Join(dir, fmt.Sprintf("iam_%s.json", sanitizeFilename(h.AccountID)))
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conversation history: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write conversation file: %w", err)
	}

	return nil
}

// Load loads conversation history from disk
func (h *ConversationHistory) Load() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	dir, err := getConversationDir()
	if err != nil {
		return err
	}

	filename := filepath.Join(dir, fmt.Sprintf("iam_%s.json", sanitizeFilename(h.AccountID)))
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// No history yet, that is fine
			return nil
		}
		return fmt.Errorf("failed to read conversation file: %w", err)
	}

	// Unmarshal into a temporary struct to avoid overwriting the mutex
	var loaded struct {
		Entries     []ConversationEntry `json:"entries"`
		AccountID   string              `json:"account_id"`
		LastSummary *AccountSummary     `json:"last_summary,omitempty"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to parse conversation history: %w", err)
	}

	h.Entries = loaded.Entries
	h.AccountID = loaded.AccountID
	h.LastSummary = loaded.LastSummary

	return nil
}

// getConversationDir returns the directory for storing conversation files
func getConversationDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".clanker", "conversations"), nil
}

// sanitizeFilename replaces characters that are invalid in filenames
func sanitizeFilename(s string) string {
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

// truncateText truncates text to maxLen characters, adding ellipsis if truncated
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
