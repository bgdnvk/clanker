package deploy

import (
	"context"
	"strings"
	"testing"
)

func TestRunIntelligenceSREOnlySkipsLLMAndInfraScan(t *testing.T) {
	calledLLM := false
	ask := func(ctx context.Context, prompt string) (string, error) {
		calledLLM = true
		t.Fatalf("SRE-only intelligence should not call the LLM; prompt=%q", prompt)
		return "", nil
	}

	result, err := RunIntelligence(
		context.Background(),
		&RepoProfile{
			RepoURL:  "https://github.com/bgdnvk/clanker",
			Summary:  "Go application",
			KeyFiles: map[string]string{},
		},
		ask,
		func(response string) string { return response },
		false,
		"aws",
		"clankercloud-tekbog",
		"us-east-2",
		&DeployOptions{SREOnly: true, DeployID: "sre-test-123"},
		func(string, ...any) {},
	)
	if err != nil {
		t.Fatalf("RunIntelligence returned error: %v", err)
	}
	if calledLLM {
		t.Fatal("SRE-only intelligence called the LLM")
	}
	if result == nil || result.DeepAnalysis == nil || result.Architecture == nil {
		t.Fatalf("expected SRE-only intelligence result, got %#v", result)
	}
	if result.InfraSnap != nil || result.CFInfraSnap != nil || result.DOInfraSnap != nil || result.HetznerInfraSnap != nil {
		t.Fatalf("SRE-only intelligence should not perform provider infra scans: %#v", result)
	}
	if result.Architecture.Method != "cloud-vm" {
		t.Fatalf("expected cloud-vm method, got %q", result.Architecture.Method)
	}
	if !strings.Contains(result.EnrichedPrompt, "Do not deploy") {
		t.Fatalf("expected no-app-deploy guard in prompt: %s", result.EnrichedPrompt)
	}
	if !strings.Contains(result.EnrichedPrompt, "--interval 60s") {
		t.Fatalf("expected conservative SRE interval in prompt: %s", result.EnrichedPrompt)
	}
}
