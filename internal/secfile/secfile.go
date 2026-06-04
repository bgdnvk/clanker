// Package secfile holds small file-permission primitives used by the
// nine provider conversation-history modules. It exists to keep the
// hardening from drifting again (issue #22): every history-file Save
// goes through WritePrivate and every Load goes through ReadPrivate,
// so adding a tenth provider can't reintroduce world-readable Q&A.
//
// This is a security primitive (file modes + Chmod-repair), not a
// conversation-history abstraction. The larger refactor — extracting
// shared Load/Save logic across providers — is tracked in #25.
package secfile

import (
	"io"
	"os"
	"runtime"
)

// PrivateDirMode and PrivateFileMode are the modes we want every
// history file and its parent directory to end up at. 0o700 / 0o600
// keeps conversation contents out of non-owner reach on shared boxes
// (CI runners, EC2 bastions, multi-UID containers).
const (
	PrivateDirMode  os.FileMode = 0o700
	PrivateFileMode os.FileMode = 0o600
)

// EnsurePrivateDir creates dir (and parents) with 0o700, then Chmods
// it to 0o700 in case it already existed with looser perms. Chmod is
// a no-op on Windows; safe to call on every Save.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, PrivateDirMode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(dir, PrivateDirMode)
}

// WritePrivate writes data to path with 0o600. If the file already
// exists with looser perms (pre-fix users), WriteFile does NOT
// re-apply the mode — so we Chmod afterwards as a belt-and-braces
// repair. Chmod is a no-op on Windows.
func WritePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, PrivateFileMode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	// Idempotent: if WriteFile honored the mode (file is new), this
	// is a no-op. If the file pre-existed with 0o644, this tightens it.
	return os.Chmod(path, PrivateFileMode)
}

// ReadPrivate opens path read-only, repairs its perms via the file
// descriptor (NOT the path — avoids TOCTOU where a local attacker
// could rename or symlink between read and chmod), then reads it
// all. Returns the file contents.
func ReadPrivate(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if runtime.GOOS != "windows" {
		// Best-effort: ignore EPERM (read-only FS, NFS without chmod).
		// We still want the read to succeed so the user isn't blocked.
		_ = f.Chmod(PrivateFileMode)
	}

	return io.ReadAll(f)
}
