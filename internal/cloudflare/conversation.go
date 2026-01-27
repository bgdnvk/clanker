package cloudflare

import (
	"context"
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

// ConversationHistory maintains conversation state for Cloudflare ask mode
type ConversationHistory struct {
	Entries    []ConversationEntry `json:"entries"`
	AccountID  string              `json:"account_id"`
	LastStatus *AccountStatus      `json:"last_status,omitempty"`
	mu         sync.RWMutex
}

// AccountStatus represents cached Cloudflare account status information
type AccountStatus struct {
	Timestamp   time.Time `json:"timestamp"`
	ZoneCount   int       `json:"zone_count"`
	WorkerCount int       `json:"worker_count"`
	TunnelCount int       `json:"tunnel_count"`
	PlanType    string    `json:"plan_type"`
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

// UpdateAccountStatus updates the cached account status
func (h *ConversationHistory) UpdateAccountStatus(status *AccountStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.LastStatus = status
}

// GetAccountStatus returns the cached account status
func (h *ConversationHistory) GetAccountStatus() *AccountStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.LastStatus
}

// GetAccountStatusContext returns a string representation of account status
// suitable for inclusion in LLM prompts
func (h *ConversationHistory) GetAccountStatusContext() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.LastStatus == nil {
		return "Account status: Not yet gathered"
	}

	return fmt.Sprintf(`Cloudflare Account Status (gathered at %s):
- Plan Type: %s
- Total Zones: %d
- Total Workers: %d
- Total Tunnels: %d`,
		h.LastStatus.Timestamp.Format("15:04:05"),
		h.LastStatus.PlanType,
		h.LastStatus.ZoneCount,
		h.LastStatus.WorkerCount,
		h.LastStatus.TunnelCount)
}

// Clear clears all conversation history
func (h *ConversationHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Entries = make([]ConversationEntry, 0)
	h.LastStatus = nil
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

	filename := filepath.Join(dir, fmt.Sprintf("cloudflare_%s.json", sanitizeFilename(h.AccountID)))
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

	filename := filepath.Join(dir, fmt.Sprintf("cloudflare_%s.json", sanitizeFilename(h.AccountID)))
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
		Entries    []ConversationEntry `json:"entries"`
		AccountID  string              `json:"account_id"`
		LastStatus *AccountStatus      `json:"last_status,omitempty"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to parse conversation history: %w", err)
	}

	h.Entries = loaded.Entries
	h.AccountID = loaded.AccountID
	h.LastStatus = loaded.LastStatus

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

// GatherAccountStatus collects basic Cloudflare account information for context
func GatherAccountStatus(ctx context.Context, client *Client) (*AccountStatus, error) {
	status := &AccountStatus{
		Timestamp: time.Now(),
	}

	// Get zones count
	zonesResp, err := client.RunAPIWithContext(ctx, "GET", "/zones", "")
	if err == nil {
		status.ZoneCount = countResultItems(zonesResp)
	}

	// Get account details for plan type
	if client.accountID != "" {
		accountResp, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/accounts/%s", client.accountID), "")
		if err == nil {
			status.PlanType = extractPlanType(accountResp)
		}
	}

	// Try to get workers count if wrangler is available
	workersResp, err := client.RunWranglerWithContext(ctx, "deployments", "list", "--json")
	if err == nil {
		status.WorkerCount = countJSONArrayItems(workersResp)
	}

	// Try to get tunnels count if cloudflared is available
	tunnelsResp, err := client.RunCloudflaredWithContext(ctx, "tunnel", "list", "--output", "json")
	if err == nil {
		status.TunnelCount = countJSONArrayItems(tunnelsResp)
	}

	return status, nil
}

// countResultItems counts items in a Cloudflare API response result array
func countResultItems(response string) int {
	var apiResp struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return 0
	}

	var items []interface{}
	if err := json.Unmarshal(apiResp.Result, &items); err != nil {
		return 0
	}

	return len(items)
}

// countJSONArrayItems counts items in a JSON array response
func countJSONArrayItems(response string) int {
	var items []interface{}
	if err := json.Unmarshal([]byte(response), &items); err != nil {
		return 0
	}
	return len(items)
}

// extractPlanType extracts the plan type from an account API response
func extractPlanType(response string) string {
	var apiResp struct {
		Success bool `json:"success"`
		Result  struct {
			Type string `json:"type"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return "unknown"
	}

	if apiResp.Result.Type != "" {
		return apiResp.Result.Type
	}

	return "unknown"
}
