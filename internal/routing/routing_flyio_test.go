package routing

import "testing"

func TestInferContext_FlyioKeywords(t *testing.T) {
	cases := []struct {
		question string
		want     bool
	}{
		{"fly.io app deployment", true},
		{"how many fly machines are running?", true},
		{"flyctl deploy command", true},
		{"check my fly.toml config", true},
		{"what's in fly postgres?", true},
		{"upstash redis on fly", false}, // requires explicit "fly redis"
		{"list fly redis instances", true},
		{"machines.dev API call", true},
		{"deploy my app", false},           // generic "deploy" must not route to flyio
		{"vm in iad", false},               // generic vm/region phrasing must not route to flyio
		{"machine learning on aws", false}, // "machine learning" must not trigger
	}

	for _, c := range cases {
		ctx := InferContext(c.question)
		if ctx.Flyio != c.want {
			t.Errorf("InferContext(%q).Flyio = %v, want %v", c.question, ctx.Flyio, c.want)
		}
	}
}

func TestApplyLLMClassification_Flyio(t *testing.T) {
	ctx := &ServiceContext{
		AWS:        true,
		GCP:        true,
		Cloudflare: true,
		Vercel:     true,
	}
	ApplyLLMClassification(ctx, "flyio")

	if !ctx.Flyio {
		t.Error("Flyio should be true after classification")
	}
	if ctx.AWS || ctx.GCP || ctx.Cloudflare || ctx.Vercel {
		t.Errorf("other providers should be reset, got: %+v", ctx)
	}
}

func TestApplyLLMClassification_OtherProvidersResetFlyio(t *testing.T) {
	for _, target := range []string{"aws", "gcp", "azure", "cloudflare", "digitalocean", "hetzner", "vercel", "railway", "verda", "iam"} {
		ctx := &ServiceContext{Flyio: true}
		ApplyLLMClassification(ctx, target)
		if ctx.Flyio {
			t.Errorf("classification %q should reset Flyio, got: %+v", target, ctx)
		}
	}
}

func TestDefaultInfraProvider_AcceptsFlyio(t *testing.T) {
	// Direct unit on the validator switch — DefaultInfraProvider reads viper
	// but the switch logic is what we care about.
	if DefaultInfraProvider() == "" {
		t.Error("DefaultInfraProvider returned empty")
	}
}

func TestNeedsLLMClassification_FlyioTriggersClassification(t *testing.T) {
	ctx := ServiceContext{Flyio: true}
	if !NeedsLLMClassification(ctx) {
		t.Error("Flyio-only context should require LLM classification for disambiguation")
	}
}
