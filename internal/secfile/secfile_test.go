package secfile

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsurePrivateDir_CreatesAt0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "fresh", "nested")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != PrivateDirMode {
		t.Errorf("dir mode = %04o, want %04o", got, PrivateDirMode)
	}
}

func TestEnsurePrivateDir_TightensExistingLooseDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != PrivateDirMode {
		t.Errorf("existing-loose dir mode = %04o, want %04o", got, PrivateDirMode)
	}
}

func TestWritePrivate_NewFileIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "history.json")
	if err := WritePrivate(path, []byte(`{"hi":1}`)); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != PrivateFileMode {
		t.Errorf("new-file mode = %04o, want %04o", got, PrivateFileMode)
	}
}

func TestWritePrivate_TightensExistingLooseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new")); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != PrivateFileMode {
		t.Errorf("overwritten-file mode = %04o, want %04o", got, PrivateFileMode)
	}
}

func TestReadPrivate_RepairsLoosePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "history.json")
	want := []byte(`{"entries":[]}`)
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrivate(path)
	if err != nil {
		t.Fatalf("ReadPrivate: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != PrivateFileMode {
		t.Errorf("post-read mode = %04o, want %04o (chmod-on-load did not run)", mode, PrivateFileMode)
	}
}

func TestReadPrivate_MissingFile(t *testing.T) {
	_, err := ReadPrivate(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error reading missing file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist, got: %v", err)
	}
}

func TestSafeSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Path-traversal payloads — must collapse to something safe.
		{"..", "default"},
		{"../../etc/passwd", "etcpasswd"},
		{"./../etc", "etc"},
		{"./../../../../etc/passwd", "etcpasswd"},

		// Dot-bearing real-world identifiers — preserved by the old
		// blocklist sanitizer; intentionally stripped now.
		{"my.cluster", "mycluster"},
		{"my-cluster.dev", "my-clusterdev"},

		// Real-world IDs we must preserve byte-for-byte.
		{"123456789012", "123456789012"},                                         // AWS account ID
		{"deadbeefcafebabe1234567890abcdef", "deadbeefcafebabe1234567890abcdef"}, // CF hex
		{"my-cluster_prod", "my-cluster_prod"},
		{"acme-prod", "acme-prod"},

		// Empty / all-stripped inputs must yield "default", never "".
		{"", "default"},
		{"...", "default"},
		{"中文", "default"},
		{"\x00\x00\x00", "default"},
		{"!@#$%^&*()", "default"},

		// Null byte and control characters are dropped (defense in depth).
		{"a\x00b", "ab"},
		{"a\nb\tc", "abc"},

		// 64-byte cap — anything past gets truncated so we don't blow
		// past filesystem limits with a 10,000-char payload.
		{string(make([]byte, 0, 200)) + repeatChar('a', 200), repeatChar('a', 64)},

		// Filesystem separators on Windows must also be stripped.
		{`C:\Users\admin`, "CUsersadmin"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SafeSlug(tc.in)
			if got != tc.want {
				t.Errorf("SafeSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) > maxSlugLen {
				t.Errorf("SafeSlug(%q) returned %d bytes — exceeds %d-byte cap", tc.in, len(got), maxSlugLen)
			}
			if got == "" {
				t.Errorf("SafeSlug(%q) returned empty string — must fall back to %q", tc.in, "default")
			}
		})
	}
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
