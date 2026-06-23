package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// EmitProgress writes a progress trace the cloud backend parses off stderr
// (same `::clanker-progress {json}` framing the AI client uses), gated by
// CLANKER_PROGRESS_TRACE so it never pollutes normal CLI output.
func EmitProgress(phase, message string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLANKER_PROGRESS_TRACE"))) {
	case "1", "true", "yes", "on":
	default:
		return
	}
	phase = strings.TrimSpace(phase)
	message = strings.TrimSpace(message)
	if phase == "" || message == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"type":      "trace",
		"phase":     phase,
		"message":   message,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stderr, "::clanker-progress "+string(payload))
}
