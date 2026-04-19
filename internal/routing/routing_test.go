package routing

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func useDefaultProvider(t *testing.T, provider string) {
	t.Helper()
	previous := viper.GetString("infra.default_provider")
	viper.Set("infra.default_provider", provider)
	t.Cleanup(func() {
		viper.Set("infra.default_provider", previous)
	})
}

func TestInferContext_DefaultBehavior_ConfiguredAWS(t *testing.T) {
	useDefaultProvider(t, "")

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

func TestInferContext_DefaultBehavior_ConfiguredHetzner(t *testing.T) {
	useDefaultProvider(t, "hetzner")

	ctx := InferContext("random question about nothing")

	if !ctx.Hetzner {
		t.Error("Unknown query should default to Hetzner=true when configured")
	}
	if ctx.AWS {
		t.Error("Unknown query should not default to AWS when Hetzner is configured")
	}
	if ctx.GitHub {
		t.Error("Unknown query should not default to GitHub when Hetzner is configured")
	}
}

func TestInferContext_GenericDiscoveryUsesConfiguredHetzner(t *testing.T) {
	useDefaultProvider(t, "hetzner")

	ctx := InferContext("what is running right now?")

	if !ctx.Hetzner {
		t.Error("Generic discovery query should use Hetzner when configured")
	}
	if ctx.AWS {
		t.Error("Generic discovery query should not force AWS when Hetzner is configured")
	}
}

func TestInferContext_CloudflareExplicit(t *testing.T) {
	useDefaultProvider(t, "")

	tests := []struct {
		name             string
		query            string
		expectCloudflare bool
		expectAWS        bool
		expectK8s        bool
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

func TestGetClassificationPrompt(t *testing.T) {
	useDefaultProvider(t, "")

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

	// contains expects an already-lowercased haystack.
	lowerPrompt := strings.ToLower(prompt)
	for _, phrase := range expectedPhrases {
		if !contains(lowerPrompt, phrase) {
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
	useDefaultProvider(t, "")

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

func TestApplyLLMClassification_GeneralUsesConfiguredDefault(t *testing.T) {
	useDefaultProvider(t, "hetzner")

	ctx := ServiceContext{}
	ApplyLLMClassification(&ctx, "general")

	if !ctx.Hetzner {
		t.Error("general classification should use configured Hetzner default")
	}
	if ctx.AWS {
		t.Error("general classification should not force AWS when Hetzner is configured")
	}
}

func TestApplyLLMClassification_GeneralPreservesGitHubContext(t *testing.T) {
	useDefaultProvider(t, "hetzner")

	ctx := ServiceContext{GitHub: true}
	ApplyLLMClassification(&ctx, "general")

	if !ctx.GitHub {
		t.Error("general classification should preserve previously inferred GitHub context")
	}
	if !ctx.Hetzner {
		t.Error("general classification should enable configured default provider")
	}
}

func TestApplyLLMClassification_GeneralPreservesTerraformContext(t *testing.T) {
	useDefaultProvider(t, "hetzner")

	ctx := ServiceContext{Terraform: true}
	ApplyLLMClassification(&ctx, "general")

	if !ctx.Terraform {
		t.Error("general classification should preserve previously inferred Terraform context")
	}
	if !ctx.Hetzner {
		t.Error("general classification should enable configured default provider")
	}
}

func TestApplyLLMClassification_GeneralPreservesK8sContext(t *testing.T) {
	useDefaultProvider(t, "")

	ctx := ServiceContext{K8s: true}
	ApplyLLMClassification(&ctx, "general")

	if !ctx.K8s {
		t.Error("general classification should preserve previously inferred K8s context")
	}
	if !ctx.AWS {
		t.Error("general classification should enable default AWS provider")
	}
}

func TestContains(t *testing.T) {
	// contains expects its first argument to already be lowercased — callers
	// in InferContext normalize the question once up front. Only the keyword
	// argument is lowercased defensively.
	tests := []struct {
		s      string
		substr string
		expect bool
	}{
		{"hello world", "world", true},
		{"hello world", "WORLD", true},
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

func TestInferContext_VerdaExplicit(t *testing.T) {
	useDefaultProvider(t, "")

	tests := []string{
		"list my verda instances",
		"how much am I spending on verda cloud",
		"any datacrunch clusters running",
		"show me the verda gpu usage",
		"spin up an instant cluster on verda",
	}
	for _, q := range tests {
		t.Run(q, func(t *testing.T) {
			ctx := InferContext(q)
			if !ctx.Verda {
				t.Errorf("expected Verda=true for %q", q)
			}
		})
	}
}

func TestInferContext_VerdaDefaultProvider(t *testing.T) {
	useDefaultProvider(t, "verda")
	// Use a query with no provider/module keywords so the default-provider
	// fallback at the end of InferContext fires.
	ctx := InferContext("what is running right now?")
	if !ctx.Verda {
		t.Error("generic discovery should activate Verda when it's the default provider")
	}
	if ctx.AWS {
		t.Error("AWS should not be activated when verda is the default")
	}
}

func TestInferContext_VerdaNoFalsePositive(t *testing.T) {
	useDefaultProvider(t, "")
	// "gpu" alone is not a Verda signal (AWS p4/p5/g5 also have GPUs).
	ctx := InferContext("list my gpu instances on aws")
	if ctx.Verda {
		t.Error("bare 'gpu' + aws should not trigger Verda routing")
	}
}

func TestApplyLLMClassification_Verda(t *testing.T) {
	useDefaultProvider(t, "")
	ctx := ServiceContext{AWS: true, Cloudflare: true, DigitalOcean: true, Hetzner: true, Vercel: true, IAM: true}
	ApplyLLMClassification(&ctx, "verda")
	if !ctx.Verda {
		t.Error("Verda should be set")
	}
	// Per the existing ApplyLLMClassification convention, the Verda case
	// clears cloud providers + IAM but preserves GitHub/Terraform.
	if ctx.AWS || ctx.Cloudflare || ctx.DigitalOcean || ctx.Hetzner || ctx.Vercel || ctx.IAM {
		t.Error("other cloud providers should be cleared when LLM picks verda")
	}
}
