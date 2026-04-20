package verda

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConversationHistorySaveLoadRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h := NewConversationHistory("project-1")
	h.AddEntry("first question", "first answer")
	h.AddEntry("second question", "second answer")

	if err := h.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := NewConversationHistory("project-1")
	if err := loaded.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded.Entries))
	}
	if loaded.Entries[0].Question != "first question" {
		t.Errorf("first entry mismatch: %+v", loaded.Entries[0])
	}
}

func TestConversationHistoryConcurrentSaves(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Fire off N goroutines writing to the same scope. The file-lock per
	// scope should serialize them; without it the final file may be a
	// half-written tmp or corrupt JSON.
	const N = 20
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			h := NewConversationHistory("project-shared")
			h.AddEntry("q", "a")
			if err := h.Save(); err != nil {
				t.Errorf("goroutine %d save: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// The final file must parse as valid JSON after the storm.
	path := filepath.Join(tmpHome, ".clanker", "conversations", "verda_project-shared.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("final file not readable: %v", err)
	}
	var got ConversationHistory
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final file not valid json: %v", err)
	}
	if got.ScopeID != "project-shared" {
		t.Errorf("wrong scope: %q", got.ScopeID)
	}
}

func TestConversationHistoryNoTmpLeaksOnSuccess(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h := NewConversationHistory("clean-up")
	h.AddEntry("q", "a")
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(tmpHome, ".clanker", "conversations"))
	if err != nil {
		t.Fatal(err)
	}
	// Expect exactly one file: the final verda_clean-up.json. Tmp files
	// should have been renamed or removed.
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file, got %d: %v", len(entries), names)
	}
}
