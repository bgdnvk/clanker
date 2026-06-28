package cmd

import "testing"

func TestRedactCredentialDisplayValue(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "api token", key: "api_token", value: "raw-token", want: "[redacted]"},
		{name: "client secret", key: "client_secret", value: "raw-secret", want: "[redacted]"},
		{name: "kubeconfig", key: "kubeconfig_content", value: "apiVersion: v1", want: "[redacted]"},
		{name: "already masked", key: "api_token", value: "tok_********1234", want: "tok_********1234"},
		{name: "public account id", key: "account_id", value: "abc123", want: "abc123"},
		{name: "empty secret", key: "api_token", value: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactCredentialDisplayValue(tt.key, tt.value); got != tt.want {
				t.Fatalf("redactCredentialDisplayValue(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}
