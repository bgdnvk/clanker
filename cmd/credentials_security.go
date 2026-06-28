package cmd

import "strings"

func redactCredentialDisplayValue(key, value string) string {
	if !isSensitiveCredentialDisplayKey(key) {
		return value
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || alreadyRedacted(trimmed) {
		return value
	}
	return "[redacted]"
}

func isSensitiveCredentialDisplayKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	for _, marker := range []string{
		"api_key",
		"api_token",
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

func alreadyRedacted(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "redacted") ||
		strings.Contains(value, "*") ||
		strings.Contains(value, "...")
}
