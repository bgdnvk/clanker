package maker

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type durableCheckpointState struct {
	UpdatedAt time.Time         `json:"updatedAt"`
	Bindings  map[string]string `json:"bindings"`
}

func loadDurableCheckpoint(plan *Plan, opts ExecOptions) (map[string]string, error) {
	checkpointPath, err := durableCheckpointPath(plan, opts)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var state durableCheckpointState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if len(state.Bindings) == 0 {
		return nil, nil
	}
	return state.Bindings, nil
}

func persistDurableCheckpoint(plan *Plan, opts ExecOptions, bindings map[string]string) error {
	checkpointPath, err := durableCheckpointPath(plan, opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		return err
	}

	state := durableCheckpointState{
		UpdatedAt: time.Now().UTC(),
		Bindings:  cloneStringMap(bindings),
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := checkpointPath + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, checkpointPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func clearDurableCheckpoint(plan *Plan, opts ExecOptions) error {
	checkpointPath, err := durableCheckpointPath(plan, opts)
	if err != nil {
		return err
	}
	err = os.Remove(checkpointPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func durableCheckpointPath(plan *Plan, opts ExecOptions) (string, error) {
	baseDir, err := os.UserHomeDir()
	if err != nil {
		baseDir = os.TempDir()
	}

	checkpointDir := filepath.Join(baseDir, ".clanker", "checkpoints")
	key, err := durableCheckpointKey(plan, opts)
	if err != nil {
		return "", err
	}
	return filepath.Join(checkpointDir, key+".json"), nil
}

func durableCheckpointKey(plan *Plan, opts ExecOptions) (string, error) {
	override := sanitizeCheckpointKey(opts.CheckpointKey)
	if override != "" {
		return override, nil
	}

	if plan == nil {
		return "", fmt.Errorf("nil plan")
	}
	commandsJSON, err := json.Marshal(plan.Commands)
	if err != nil {
		return "", err
	}

	hashInput := strings.Join([]string{
		strings.TrimSpace(opts.Profile),
		strings.TrimSpace(opts.Region),
		string(commandsJSON),
	}, "\n")
	sum := sha1.Sum([]byte(hashInput))
	return "aws-" + hex.EncodeToString(sum[:]), nil
}

func sanitizeCheckpointKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	allowed := make([]rune, 0, len(key))
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			allowed = append(allowed, r)
			continue
		}
		allowed = append(allowed, '-')
	}
	cleaned := strings.Trim(strings.Join(strings.Fields(strings.ReplaceAll(string(allowed), "--", "-")), "-"), "-")
	if cleaned == "" {
		return ""
	}
	return cleaned
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}
