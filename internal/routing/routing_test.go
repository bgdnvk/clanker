package routing

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
			ctx := InferContext(tt.query)

			if ctx.Cloudflare != tt.expectCloudflare {
				t.Errorf("InferContext(%q) cloudflare = %v, want %v", tt.query, ctx.Cloudflare, tt.expectCloudflare)
			}
			if ctx.AWS != tt.expectAWS {
				t.Errorf("InferContext(%q) aws = %v, want %v", tt.query, ctx.AWS, tt.expectAWS)
			}
			if ctx.K8s != tt.expectK8s {
				t.Errorf("InferContext(%q) k8s = %v, want %v", tt.query, ctx.K8s, tt.expectK8s)
			}
		})
	}
}

func TestInferContext_NoCloudflarefalsePositives(t *testing.T) {
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
			ctx := InferContext(query)
			if ctx.Cloudflare {
				t.Errorf("InferContext(%q) incorrectly triggered Cloudflare routing", query)
			}
		})
	}
}

func TestInferContext_DefaultBehavior(t *testing.T) {
	// Unknown queries should default to AWS + GitHub
	ctx := InferContext("random question about nothing")

	if !ctx.AWS {
		t.Error("Unknown query should default to AWS=true")
	}
	if !ctx.GitHub {
		t.Error("Unknown query should default to GitHub=true")
	}
	if ctx.Cloudflare {
		t.Error("Unknown query should not trigger Cloudflare")
	}
	if ctx.K8s {
		t.Error("Unknown query should not trigger K8s")
	}
}

func TestGetClassificationPrompt(t *testing.T) {
	prompt := GetClassificationPrompt("list my cloudflare zones")

	if prompt == "" {
		t.Error("GetClassificationPrompt returned empty string")
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
			t.Errorf("GetClassificationPrompt missing expected phrase: %s", phrase)
		}
	}
}

func TestNeedsLLMClassification(t *testing.T) {
	tests := []struct {
		name   string
		ctx    ServiceContext
		expect bool
	}{
		{
			name:   "cloudflare detected needs verification",
			ctx:    ServiceContext{Cloudflare: true},
			expect: true,
		},
		{
			name:   "multiple services need disambiguation",
			ctx:    ServiceContext{AWS: true, K8s: true},
			expect: true,
		},
		{
			name:   "single aws does not need llm",
			ctx:    ServiceContext{AWS: true},
			expect: false,
		},
		{
			name:   "single k8s does not need llm",
			ctx:    ServiceContext{K8s: true},
			expect: false,
		},
		{
			name:   "aws and cloudflare needs llm",
			ctx:    ServiceContext{AWS: true, Cloudflare: true},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NeedsLLMClassification(tt.ctx)
			if result != tt.expect {
				t.Errorf("NeedsLLMClassification(%+v) = %v, want %v", tt.ctx, result, tt.expect)
			}
		})
	}
}

func TestApplyLLMClassification(t *testing.T) {
	tests := []struct {
		name       string
		llmService string
		expectAWS  bool
		expectCF   bool
		expectK8s  bool
		expectGCP  bool
	}{
		{
			name:       "cloudflare classification",
			llmService: "cloudflare",
			expectCF:   true,
			expectAWS:  false,
			expectK8s:  false,
			expectGCP:  false,
		},
		{
			name:       "aws classification",
			llmService: "aws",
			expectAWS:  true,
			expectCF:   false,
			expectK8s:  false,
			expectGCP:  false,
		},
		{
			name:       "k8s classification",
			llmService: "k8s",
			expectK8s:  true,
			expectAWS:  false,
			expectCF:   false,
			expectGCP:  false,
		},
		{
			name:       "gcp classification",
			llmService: "gcp",
			expectGCP:  true,
			expectAWS:  false,
			expectCF:   false,
			expectK8s:  false,
		},
		{
			name:       "general defaults to aws",
			llmService: "general",
			expectAWS:  true,
			expectCF:   false,
			expectK8s:  false,
			expectGCP:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ServiceContext{}
			ApplyLLMClassification(&ctx, tt.llmService)

			if ctx.AWS != tt.expectAWS {
				t.Errorf("ApplyLLMClassification(%q) AWS = %v, want %v", tt.llmService, ctx.AWS, tt.expectAWS)
			}
			if ctx.Cloudflare != tt.expectCF {
				t.Errorf("ApplyLLMClassification(%q) Cloudflare = %v, want %v", tt.llmService, ctx.Cloudflare, tt.expectCF)
			}
			if ctx.K8s != tt.expectK8s {
				t.Errorf("ApplyLLMClassification(%q) K8s = %v, want %v", tt.llmService, ctx.K8s, tt.expectK8s)
			}
			if ctx.GCP != tt.expectGCP {
				t.Errorf("ApplyLLMClassification(%q) GCP = %v, want %v", tt.llmService, ctx.GCP, tt.expectGCP)
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		expect bool
	}{
		{"Hello World", "world", true},
		{"Hello World", "WORLD", true},
		{"cloudflare zones", "cloudflare", true},
		{"list ec2", "EC2", true},
		{"kubernetes pods", "k8s", false},
		{"", "test", false},
		{"test", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.substr, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expect {
				t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, result, tt.expect)
			}
		})
	}
}
