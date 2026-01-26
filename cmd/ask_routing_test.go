package cmd

import (
	"testing"
)

func TestInferContext_CloudflareExplicit(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		expectCloudflare  bool
		expectAWS         bool
		expectK8s         bool
	}{
		{
			name:             "explicit cloudflare mention",
			query:            "list my cloudflare zones",
			expectCloudflare: true,
			expectAWS:        false,
			expectK8s:        false,
		},
		{
			name:             "wrangler tool mention",
			query:            "wrangler deploy my worker",
			expectCloudflare: true,
			expectAWS:        false,
			expectK8s:        false,
		},
		{
			name:             "cloudflared tool mention",
			query:            "cloudflared tunnel list",
			expectCloudflare: true,
			expectAWS:        false,
			expectK8s:        false,
		},
		{
			name:             "generic cache should not trigger cloudflare",
			query:            "show cache hit rate",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "generic cdn should not trigger cloudflare",
			query:            "list cdn distributions",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "generic worker should not trigger cloudflare",
			query:            "show worker processes",
			expectCloudflare: false,
			expectAWS:        false,
			expectK8s:        false,
		},
		{
			name:             "generic waf should not trigger cloudflare",
			query:            "list waf rules",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "generic rate limit should not trigger cloudflare",
			query:            "show rate limits",
			expectCloudflare: false,
			expectAWS:        true, // "rate" triggers AWS keyword match
			expectK8s:        false,
		},
		{
			name:             "generic dns should not trigger cloudflare",
			query:            "list dns records",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "ec2 should trigger aws",
			query:            "list ec2 instances",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "lambda should trigger aws",
			query:            "show lambda functions",
			expectCloudflare: false,
			expectAWS:        true,
			expectK8s:        false,
		},
		{
			name:             "pods should trigger k8s",
			query:            "list pods",
			expectCloudflare: false,
			expectAWS:        false,
			expectK8s:        true,
		},
		{
			name:             "kubernetes should trigger k8s",
			query:            "show kubernetes deployments",
			expectCloudflare: false,
			expectAWS:        false,
			expectK8s:        true,
		},
		{
			name:             "kubectl should trigger k8s",
			query:            "kubectl get nodes",
			expectCloudflare: false,
			expectAWS:        false,
			expectK8s:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aws, _, _, _, k8s, _, cf := inferContext(tt.query)

			if cf != tt.expectCloudflare {
				t.Errorf("inferContext(%q) cloudflare = %v, want %v", tt.query, cf, tt.expectCloudflare)
			}
			if aws != tt.expectAWS {
				t.Errorf("inferContext(%q) aws = %v, want %v", tt.query, aws, tt.expectAWS)
			}
			if k8s != tt.expectK8s {
				t.Errorf("inferContext(%q) k8s = %v, want %v", tt.query, k8s, tt.expectK8s)
			}
		})
	}
}

func TestInferContext_NoFalsePositives(t *testing.T) {
	// These queries should NOT trigger Cloudflare routing
	noCloudflareQueries := []string{
		"what is the cache hit rate",
		"show cdn distribution",
		"list workers",
		"show rate limits",
		"check waf status",
		"create tunnel to database",
		"show analytics dashboard",
		"configure access control",
		"deploy to pages",
		"list dns records for route53",
		"show cloudfront distributions",
	}

	for _, query := range noCloudflareQueries {
		t.Run(query, func(t *testing.T) {
			_, _, _, _, _, _, cf := inferContext(query)
			if cf {
				t.Errorf("inferContext(%q) incorrectly triggered Cloudflare routing", query)
			}
		})
	}
}

func TestGetRoutingClassificationPrompt(t *testing.T) {
	prompt := getRoutingClassificationPrompt("list my cloudflare zones")

	// Check that the prompt contains expected elements
	if prompt == "" {
		t.Error("getRoutingClassificationPrompt returned empty string")
	}

	expectedPhrases := []string{
		"cloudflare",
		"aws",
		"k8s",
		"gcp",
		"JSON object",
		"service",
	}

	for _, phrase := range expectedPhrases {
		if !contains(prompt, phrase) {
			t.Errorf("getRoutingClassificationPrompt missing expected phrase: %s", phrase)
		}
	}
}
