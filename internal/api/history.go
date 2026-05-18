package api

import (
	"sync"
	"time"
)

const (
	// historyCap is the ring buffer size — small enough to keep in memory,
	// big enough to cover a working session. Persistence to disk is a v2
	// concern; for now the history clears when the server restarts.
	historyCap = 50

	// outputTruncateBytes caps what we keep per record. Apply output is
	// already small (a few KB), but a multi-command plan could grow.
	outputTruncateBytes = 16 << 10 // 16 KiB
)

// ApplyRecord is what /api/v1/maker/history returns for each past apply.
//
// Output is truncated server-side so the list endpoint stays cheap. For full
// output a future endpoint can fetch by ID.
type ApplyRecord struct {
	ID               int64     `json:"id"`
	StartedAt        time.Time `json:"started_at"`
	Provider         string    `json:"provider"`
	Status           string    `json:"status"` // "ok" or "error"
	Duration         string    `json:"duration"`
	Destroyer        bool      `json:"destroyer"`
	CommandCount     int       `json:"command_count"`
	DestructiveCount int       `json:"destructive_count"`
	Summary          string    `json:"summary,omitempty"`
	Question         string    `json:"question,omitempty"`
	Error            string    `json:"error,omitempty"`
	Output           string    `json:"output,omitempty"`
	OutputTruncated  bool      `json:"output_truncated,omitempty"`
}

// history is the singleton ring buffer. Concurrent appends from any handler
// are safe; reads return a defensive copy so the response can be marshalled
// without holding the lock.
type history struct {
	mu      sync.Mutex
	items   []ApplyRecord
	nextID  int64
}

func newHistory() *history {
	return &history{items: make([]ApplyRecord, 0, historyCap)}
}

func (h *history) append(rec ApplyRecord) ApplyRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	rec.ID = h.nextID
	if len(rec.Output) > outputTruncateBytes {
		rec.Output = rec.Output[:outputTruncateBytes]
		rec.OutputTruncated = true
	}
	if len(h.items) >= historyCap {
		// drop oldest
		copy(h.items, h.items[1:])
		h.items = h.items[:len(h.items)-1]
	}
	h.items = append(h.items, rec)
	return rec
}

// list returns records newest-first. limit<=0 means everything.
func (h *history) list(limit int) []ApplyRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.items)
	out := make([]ApplyRecord, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, h.items[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
