package deploy

import (
	"strings"
	"testing"
)

func TestRenderOpenClawDOBootstrapScriptInstallsGitAndComposePlugin(t *testing.T) {
	script := renderOpenClawDOBootstrapScript(openClawDOBootstrapSpec{
		RepoURL:          "https://github.com/openclaw/openclaw",
		GatewaySecretKey: "OPENCLAW_GATEWAY_TOKEN",
		IncludeAnthropic: true,
		IncludeDiscord:   true,
		IncludeTelegram:  true,
	})

	if !strings.Contains(script, "apt-get install -y git docker-compose-plugin") {
		t.Fatalf("expected bootstrap to install git and docker-compose-plugin, got %q", script)
	}
	if !strings.Contains(script, "git clone https://github.com/openclaw/openclaw /opt/openclaw") {
		t.Fatalf("expected bootstrap to clone repo, got %q", script)
	}
	if !strings.Contains(script, "docker compose up -d openclaw-gateway") {
		t.Fatalf("expected bootstrap to use docker compose, got %q", script)
	}
	if !strings.Contains(script, "OPENCLAW_IMAGE=ghcr.io/openclaw/openclaw:latest") {
		t.Fatalf("expected bootstrap to pin the upstream GHCR image, got %q", script)
	}
	if !strings.Contains(script, "docker compose pull openclaw-gateway") {
		t.Fatalf("expected bootstrap to pull the upstream image before starting, got %q", script)
	}
	if strings.Contains(script, "apt-get install -y docker-compose\n") {
		t.Fatalf("expected standalone docker-compose package install to be removed, got %q", script)
	}
	if strings.Contains(script, "docker build -t openclaw:local") {
		t.Fatalf("expected local source build to be removed from bootstrap, got %q", script)
	}
}
