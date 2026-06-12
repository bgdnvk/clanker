package sshknownhosts

import (
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestTOFUCallbackRecordsAndRejectsChangedHostKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}

	path := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	first := testSigner(t)
	callback, err := NewTOFUCallback(path)
	if err != nil {
		t.Fatalf("NewTOFUCallback: %v", err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	if err := callback("127.0.0.1:22", remote, first.PublicKey()); err != nil {
		t.Fatalf("first host key callback: %v", err)
	}
	assertKnownHostsPerm(t, filepath.Dir(path), 0o700)
	assertKnownHostsPerm(t, path, 0o600)

	if err := callback("127.0.0.1:22", remote, first.PublicKey()); err != nil {
		t.Fatalf("known host callback: %v", err)
	}
	changed := testSigner(t)
	if err := callback("127.0.0.1:22", remote, changed.PublicKey()); err == nil {
		t.Fatal("changed host key was accepted")
	}
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func assertKnownHostsPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
	}
}
