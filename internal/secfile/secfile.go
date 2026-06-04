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

// maxSlugLen bounds slugs so a malicious or merely verbose identifier
// can't produce a filename longer than common filesystem limits
// (ext4: 255, HFS+: 255, NTFS: 255). 64 is well under that and covers
// every real-world ID we sanitize — AWS account IDs (12 digits),
// Cloudflare account IDs (32 hex), kubectl context names (typically
// under 63 per DNS label limit).
const maxSlugLen = 64

// SafeSlug strips every byte outside [A-Za-z0-9_-] from s so that
// caller-supplied identifiers (cluster names, account IDs, org slugs)
// cannot escape ~/.clanker via path traversal. Inputs like
// "../../etc/passwd" collapse to "etcpasswd"; "my.cluster" → "mycluster".
//
// We iterate as bytes rather than runes intentionally: multi-byte
// UTF-8 codepoints all fall outside the ASCII allowlist, so they
// would be dropped byte-by-byte either way. Operating on bytes keeps
// the function predictable and ~5x cheaper, and matches the historic
// pattern in the three providers that got this right (sentry, linear,
// notion).
//
// The result is bounded to 64 bytes (see maxSlugLen). An empty result
// returns "default" so the caller never produces a hidden file
// (".json") or an empty filename.
//
// Note on collisions: distinct identifiers that differ only in
// stripped characters (e.g. "acme/prod" and "acme-prod") will produce
// the same slug. In practice the inputs are constrained (UUIDs, hex
// IDs, 12-digit AWS account numbers) so collisions are exceedingly
// rare. The cross-tenant "default" fallback collision (two unset
// identifiers both writing to the same history file) is tracked in
// #25 and deliberately out of scope here.
func SafeSlug(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_':
			out = append(out, c)
		}
	}
	if len(out) > maxSlugLen {
		out = out[:maxSlugLen]
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}
