package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const progressTracePrefix = "::clanker-progress "

type progressTraceEvent struct {
	Type      string `json:"type"`
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func progressTraceEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLANKER_PROGRESS_TRACE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func emitProgressTrace(phase, message string) {
	if !progressTraceEnabled() {
		return
	}
	phase = strings.TrimSpace(phase)
	message = strings.TrimSpace(message)
	if phase == "" || message == "" {
		return
	}
	payload, err := json.Marshal(progressTraceEvent{
		Type:      "trace",
		Phase:     phase,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stderr, progressTracePrefix+string(payload))
}
