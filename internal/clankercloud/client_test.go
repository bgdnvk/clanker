package clankercloud

import "testing"

func TestNormalizeLocalAPIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "localhost", raw: "http://localhost:8080/api/", want: "http://localhost:8080/api"},
		{name: "ipv4 loopback", raw: "http://127.0.0.1:8080/api", want: "http://127.0.0.1:8080/api"},
		{name: "ipv6 loopback", raw: "http://[::1]:8080/api", want: "http://[::1]:8080/api"},
		{name: "remote host", raw: "https://example.com/api", wantErr: true},
		{name: "userinfo", raw: "http://user:pass@localhost:8080/api", wantErr: true},
		{name: "query", raw: "http://localhost:8080/api?x=1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeLocalAPIBaseURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeLocalAPIBaseURL(%q) succeeded, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLocalAPIBaseURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeLocalAPIBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
