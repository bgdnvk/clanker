package cost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExporterWritesPrivateFiles(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nested", "cost.json")
	if err := NewExporter().ExportToFile(map[string]string{"account": "prod"}, "json", outPath); err != nil {
		t.Fatalf("export: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Dir(outPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0750 {
		t.Fatalf("dir mode = %v, want 0750", got)
	}

	fileInfo, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("file mode = %v, want 0600", got)
	}
}
