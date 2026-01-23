package k8s

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
	Cluster   string    `json:"cluster"`
}

// ConversationHistory maintains conversation state for K8s ask mode
type ConversationHistory struct {
	Entries     []ConversationEntry `json:"entries"`
	ClusterName string              `json:"cluster_name"`
	LastStatus  *ClusterStatus      `json:"last_status,omitempty"`
	mu          sync.RWMutex
}

// ClusterStatus represents cached cluster status information
type ClusterStatus struct {
	Timestamp      time.Time `json:"timestamp"`
	NodeCount      int       `json:"node_count"`
	PodCount       int       `json:"pod_count"`
	NamespaceCount int       `json:"namespace_count"`
	Version        string    `json:"version"`
	Context        string    `json:"context"`
}

// MaxHistoryEntries limits the conversation history size
const MaxHistoryEntries = 20

// MaxAnswerLengthInContext limits how much of previous answers to include in context
const MaxAnswerLengthInContext = 500

// NewConversationHistory creates a new conversation history for a cluster
func NewConversationHistory(clusterName string) *ConversationHistory {
	return &ConversationHistory{
		Entries:     make([]ConversationEntry, 0),
		ClusterName: clusterName,
	}
}

// AddEntry adds a new conversation entry
func (h *ConversationHistory) AddEntry(question, answer, cluster string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := ConversationEntry{
		Timestamp: time.Now(),
		Question:  question,
		Answer:    answer,
		Cluster:   cluster,
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

// UpdateClusterStatus updates the cached cluster status
func (h *ConversationHistory) UpdateClusterStatus(status *ClusterStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.LastStatus = status
}

// GetClusterStatus returns the cached cluster status
func (h *ConversationHistory) GetClusterStatus() *ClusterStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.LastStatus
}

// GetClusterStatusContext returns a string representation of cluster status
// suitable for inclusion in LLM prompts
func (h *ConversationHistory) GetClusterStatusContext() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.LastStatus == nil {
		return "Cluster status: Not yet gathered"
	}

	return fmt.Sprintf(`Cluster Status (gathered at %s):
- Current Context: %s
- Kubernetes Version: %s
- Total Nodes: %d
- Total Pods: %d
- Total Namespaces: %d`,
		h.LastStatus.Timestamp.Format("15:04:05"),
		h.LastStatus.Context,
		h.LastStatus.Version,
		h.LastStatus.NodeCount,
		h.LastStatus.PodCount,
		h.LastStatus.NamespaceCount)
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

	filename := filepath.Join(dir, fmt.Sprintf("k8s_%s.json", sanitizeFilename(h.ClusterName)))
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

	filename := filepath.Join(dir, fmt.Sprintf("k8s_%s.json", sanitizeFilename(h.ClusterName)))
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
		ClusterName string              `json:"cluster_name"`
		LastStatus  *ClusterStatus      `json:"last_status,omitempty"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to parse conversation history: %w", err)
	}

	h.Entries = loaded.Entries
	h.ClusterName = loaded.ClusterName
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

// GatherClusterStatus collects basic cluster information for context
func GatherClusterStatus(ctx context.Context, client *Client) (*ClusterStatus, error) {
	status := &ClusterStatus{
		Timestamp: time.Now(),
	}

	// Get current context
	currentCtx, err := client.GetCurrentContext(ctx)
	if err == nil {
		status.Context = currentCtx
	}

	// Get version (extract just the server version line)
	version, err := client.GetVersion(ctx)
	if err == nil {
		status.Version = extractServerVersion(version)
	}

	// Get node count
	nodes, err := client.GetNodes(ctx)
	if err == nil {
		status.NodeCount = len(nodes)
	}

	// Get namespace count
	namespaces, err := client.GetNamespaces(ctx)
	if err == nil {
		status.NamespaceCount = len(namespaces)
	}

	// Get pod count (across all namespaces)
	podsOutput, err := client.Run(ctx, "get", "pods", "-A", "--no-headers")
	if err == nil {
		lines := strings.Split(strings.TrimSpace(podsOutput), "\n")
		if len(lines) == 1 && lines[0] == "" {
			status.PodCount = 0
		} else {
			status.PodCount = len(lines)
		}
	}

	return status, nil
}

// extractServerVersion extracts just the server version from kubectl version output
func extractServerVersion(versionOutput string) string {
	lines := strings.Split(versionOutput, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Server Version") {
			return line
		}
		// Handle JSON output format
		if strings.Contains(line, "serverVersion") || strings.Contains(line, "gitVersion") {
			return line
		}
	}
	// If no server version found, return first non-empty line
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return versionOutput
}
