package cmd

import (
	"os"
	"runtime"
	"strings"
)

func writePrivateUserConfig(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return hardenUserConfigFile(path)
}

func hardenUserConfigFile(path string) error {
	if strings.TrimSpace(path) == "" || runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, 0o600)
}

func redactConfigForDisplay(content []byte) string {
	lines := strings.SplitAfter(string(content), "\n")
	for i, line := range lines {
		lines[i] = redactConfigLine(line)
	}
	return strings.Join(lines, "")
}

func redactConfigLine(line string) string {
	body := strings.TrimRight(line, "\r\n")
	suffix := line[len(body):]
	trimmed := strings.TrimLeft(body, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return line
	}

	colon := strings.Index(trimmed, ":")
	if colon < 0 {
		return line
	}
	key := strings.Trim(strings.TrimSpace(trimmed[:colon]), `"'`)
	if !isSensitiveConfigDisplayKey(key) {
		return line
	}
	value := strings.TrimSpace(trimmed[colon+1:])
	if value == "" || value == `""` || value == "''" {
		return line
	}

	indent := body[:len(body)-len(trimmed)]
	return indent + trimmed[:colon+1] + " [redacted]" + suffix
}

func isSensitiveConfigDisplayKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	if strings.HasSuffix(k, "_env") {
		return false
	}
	for _, marker := range []string{
		"api_key",
		"access_key",
		"client_secret",
		"credential",
		"kubeconfig",
		"password",
		"private_key",
		"secret",
		"session_token",
		"token",
	} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}
