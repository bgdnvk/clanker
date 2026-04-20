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

// fileLocks serializes Save+Load per ScopeID so two concurrent
// `clanker verda ask` invocations for the same project don't race on the
// tmp-file + rename dance and lose one conversation's history. Keyed by the
// sanitized filename so two scopes that happen to normalise to the same name
// still share a lock.
var (
	fileLocks   = map[string]*sync.Mutex{}
	fileLocksMu sync.Mutex
)

func fileLockFor(name string) *sync.Mutex {
	fileLocksMu.Lock()
	defer fileLocksMu.Unlock()
	if m, ok := fileLocks[name]; ok {
		return m
	}
	m := &sync.Mutex{}
	fileLocks[name] = m
	return m
}

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
// Write is atomic: marshal → write temp file → rename over the destination.
// A crash mid-write leaves either the old or new file intact but never a
// half-written blob that would poison subsequent loads. A per-scope file lock
// serializes concurrent Save calls for the same project — without it, two
// parallel `ask --verda` calls could race on the rename and drop one history.
func (h *ConversationHistory) Save() error {
	h.mu.RLock()
	data, err := json.MarshalIndent(h, "", "  ")
	scopeName := sanitize(h.ScopeID)
	h.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}

	lock := fileLockFor(scopeName)
	lock.Lock()
	defer lock.Unlock()

	dir, err := conversationDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create conversation dir: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("verda_%s.json", scopeName))
	tmp, err := os.CreateTemp(dir, "verda_*.json.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Load restores history from disk. Missing file is not an error.
func (h *ConversationHistory) Load() error {
	// Take the per-scope file lock first so we don't read a file that Save
	// is currently mid-rename on. Grabbing the struct mutex early would let
	// a parallel Save hold fileLock + block here, so the order is:
	// fileLock (cross-process write barrier) → struct mu (in-memory state).
	scopeName := sanitize(h.ScopeID)
	lock := fileLockFor(scopeName)
	lock.Lock()
	defer lock.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()

	dir, err := conversationDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("verda_%s.json", scopeName))
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
