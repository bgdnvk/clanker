package cluster

import (
	"testing"
)

func TestGetSecurityWarning(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantNone bool
	}{
		{
			name: "public endpoint warning",
			key:  "public-endpoint",
		},
		{
			name: "public service warning",
			key:  "public-service",
		},
		{
			name: "no network policy warning",
			key:  "no-network-policy",
		},
		{
			name: "secrets warning",
			key:  "secrets-plain",
		},
		{
			name: "root container warning",
			key:  "root-container",
		},
		{
			name: "privileged warning",
			key:  "privileged",
		},
		{
			name: "host network warning",
			key:  "host-network",
		},
		{
			name: "no resource limits warning",
			key:  "no-resource-limits",
		},
		{
			name:     "unknown key returns empty",
			key:      "unknown-key",
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning := GetSecurityWarning(tt.key)
			if tt.wantNone {
				if warning != "" {
					t.Errorf("GetSecurityWarning(%q) = %q, want empty", tt.key, warning)
				}
			} else {
				if warning == "" {
					t.Errorf("GetSecurityWarning(%q) returned empty, want warning", tt.key)
				}
			}
		})
	}
}

func TestGetSecurityRecommendations(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		wantNone bool
	}{
		{
			name:     "new cluster recommendations",
			scenario: "new-cluster",
		},
		{
			name:     "new deployment recommendations",
			scenario: "new-deployment",
		},
		{
			name:     "new service recommendations",
			scenario: "new-service",
		},
		{
			name:     "secrets recommendations",
			scenario: "secrets",
		},
		{
			name:     "unknown scenario returns nil",
			scenario: "unknown-scenario",
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := GetSecurityRecommendations(tt.scenario)
			if tt.wantNone {
				if recs != nil {
					t.Errorf("GetSecurityRecommendations(%q) = %v, want nil", tt.scenario, recs)
				}
			} else {
				if recs == nil || len(recs) == 0 {
					t.Errorf("GetSecurityRecommendations(%q) returned empty, want recommendations", tt.scenario)
				}
			}
		})
	}
}

func TestIsPublicEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		opts     CreateOptions
		expected bool
	}{
		{
			name: "only public endpoint enabled",
			opts: CreateOptions{
				EnablePublicEndpoint:  true,
				EnablePrivateEndpoint: false,
			},
			expected: true,
		},
		{
			name: "both endpoints enabled",
			opts: CreateOptions{
				EnablePublicEndpoint:  true,
				EnablePrivateEndpoint: true,
			},
			expected: false,
		},
		{
			name: "only private endpoint enabled",
			opts: CreateOptions{
				EnablePublicEndpoint:  false,
				EnablePrivateEndpoint: true,
			},
			expected: false,
		},
		{
			name: "neither endpoint enabled",
			opts: CreateOptions{
				EnablePublicEndpoint:  false,
				EnablePrivateEndpoint: false,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPublicEndpoint(tt.opts)
			if result != tt.expected {
				t.Errorf("IsPublicEndpoint() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestShouldWarnPublicAccess(t *testing.T) {
	tests := []struct {
		name     string
		opts     CreateOptions
		expected bool
	}{
		{
			name: "warn when only public endpoint",
			opts: CreateOptions{
				EnablePublicEndpoint:  true,
				EnablePrivateEndpoint: false,
			},
			expected: true,
		},
		{
			name: "no warn when both endpoints",
			opts: CreateOptions{
				EnablePublicEndpoint:  true,
				EnablePrivateEndpoint: true,
			},
			expected: false,
		},
		{
			name: "no warn when only private",
			opts: CreateOptions{
				EnablePublicEndpoint:  false,
				EnablePrivateEndpoint: true,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldWarnPublicAccess(tt.opts)
			if result != tt.expected {
				t.Errorf("ShouldWarnPublicAccess() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestSecurityWarningsContent(t *testing.T) {
	// Verify that security warnings contain expected keywords
	expectedKeywords := map[string]string{
		"public-endpoint":    "public",
		"public-service":     "LoadBalancer",
		"no-network-policy":  "network policy",
		"secrets-plain":      "secrets",
		"root-container":     "root",
		"privileged":         "Privileged",
		"host-network":       "host network",
		"no-resource-limits": "resource limits",
	}

	for key, keyword := range expectedKeywords {
		t.Run(key, func(t *testing.T) {
			warning := GetSecurityWarning(key)
			if warning == "" {
				t.Fatalf("GetSecurityWarning(%q) returned empty", key)
			}
			if !containsIgnoreCase(warning, keyword) {
				t.Errorf("GetSecurityWarning(%q) = %q, does not contain %q", key, warning, keyword)
			}
		})
	}
}

func containsIgnoreCase(s, substr string) bool {
	// Simple case-insensitive contains check
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if toLower(s[i+j]) != toLower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
