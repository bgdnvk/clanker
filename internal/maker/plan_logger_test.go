package maker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPlanLogWriterUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	w, err := NewPlanLogWriter("run-private")
	if err != nil {
		t.Fatalf("NewPlanLogWriter: %v", err)
	}
	_, _ = w.Write([]byte("deployment output\n"))
	plan := &Plan{
		Version:  CurrentPlanVersion,
		Question: "deploy app",
		Commands: []Command{
			{Args: []string{"echo", "ok"}},
		},
	}
	if err := w.WritePlan(plan); err != nil {
		t.Fatalf("WritePlan: %v", err)
	}
	if err := w.WriteSummary("ok", plan); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	logDir := filepath.Join(home, ".clanker", "logs", "plan", "run-private")
	assertPerm(t, logDir, 0o700)
	for _, name := range []string{"output.log", "events.log", "fixes.log", "plan.json", "summary.json"} {
		assertPerm(t, filepath.Join(logDir, name), 0o600)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
	}
}
