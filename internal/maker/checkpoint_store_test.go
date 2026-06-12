package maker

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDurableCheckpointUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	plan := &Plan{Commands: []Command{{Args: []string{"aws", "ec2", "describe-instances"}}}}
	opts := ExecOptions{CheckpointKey: "private-checkpoint"}
	if err := persistDurableCheckpoint(plan, opts, map[string]string{
		"INSTANCE_ID": "i-123",
		"ENV_TOKEN":   "must-not-persist",
	}); err != nil {
		t.Fatalf("persistDurableCheckpoint: %v", err)
	}

	checkpointPath := filepath.Join(home, ".clanker", "checkpoints", "private-checkpoint.json")
	assertPerm(t, filepath.Dir(checkpointPath), 0o700)
	assertPerm(t, checkpointPath, 0o600)

	state, err := loadDurableCheckpoint(plan, opts)
	if err != nil {
		t.Fatalf("loadDurableCheckpoint: %v", err)
	}
	if state["INSTANCE_ID"] != "i-123" {
		t.Fatalf("INSTANCE_ID = %q, want i-123", state["INSTANCE_ID"])
	}
	if _, ok := state["ENV_TOKEN"]; ok {
		t.Fatal("ENV_TOKEN should not persist in durable checkpoint")
	}
}
