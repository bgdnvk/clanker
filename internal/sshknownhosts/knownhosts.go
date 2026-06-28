package sshknownhosts

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// NewTOFUCallback verifies SSH host keys against known_hosts and records
// unknown hosts on first use. Changed or revoked host keys still fail.
func NewTOFUCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home directory for known_hosts: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		callback, err := knownhosts.New(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return appendHostKey(path, hostname, remote, key)
			}
			return fmt.Errorf("load known_hosts: %w", err)
		}
		err = callback(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			return appendHostKey(path, hostname, remote, key)
		}
		return err
	}, nil
}

func appendHostKey(path string, hostname string, remote net.Addr, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if runtime.GOOS != "windows" {
		_ = f.Chmod(0o600)
	}
	_, err = fmt.Fprintln(f, knownhosts.Line(hostKeyAddresses(hostname, remote), key))
	return err
}

func hostKeyAddresses(hostname string, remote net.Addr) []string {
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		normalized := knownhosts.Normalize(value)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	add(hostname)
	if remote != nil {
		add(remote.String())
	}
	return out
}
