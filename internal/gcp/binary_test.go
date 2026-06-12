package gcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGcloudBinaryRequiresAbsoluteOverride(t *testing.T) {
	t.Setenv("CLANKER_GCLOUD_PATH", "relative/gcloud")
	if _, err := FindGcloudBinary(); err == nil {
		t.Fatal("FindGcloudBinary accepted relative CLANKER_GCLOUD_PATH")
	}
}

func TestFindGcloudBinaryAcceptsAbsoluteOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gcloud")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLANKER_GCLOUD_PATH", path)
	got, err := FindGcloudBinary()
	if err != nil {
		t.Fatalf("FindGcloudBinary: %v", err)
	}
	if got != path {
		t.Fatalf("FindGcloudBinary = %q, want %q", got, path)
	}
}
