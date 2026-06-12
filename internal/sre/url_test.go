package sre

import "testing"

func TestNormalizeAPIBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root", raw: "https://clanker.example", want: "https://clanker.example/api"},
		{name: "already api", raw: "https://clanker.example/api/", want: "https://clanker.example/api"},
		{name: "nested", raw: "https://clanker.example/base", want: "https://clanker.example/base/api"},
		{name: "empty", raw: "", want: ""},
		{name: "no scheme", raw: "clanker.example", want: ""},
		{name: "userinfo", raw: "https://token@clanker.example", want: ""},
		{name: "query", raw: "https://clanker.example/api?x=1", want: ""},
		{name: "bad scheme", raw: "file:///tmp/socket", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeAPIBaseURL(tt.raw); got != tt.want {
				t.Fatalf("NormalizeAPIBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
