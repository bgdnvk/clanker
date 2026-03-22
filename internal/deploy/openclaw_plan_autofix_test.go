package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/maker"
)

func TestFixOpenClawDOAppPlatformProxyRemovesDockerTag(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Commands: []maker.Command{
			{Args: []string{"compute", "droplet", "create", "openclaw", "--region", "nyc1"}},
			{Args: []string{"registry", "create", "openclaw-registry"}, Produces: map[string]string{"REGISTRY_NAME": "name"}},
			{Args: []string{"docker", "build", "-t", "openclaw-proxy:latest", "."}},
			{Args: []string{"docker", "tag", "openclaw-proxy:latest", "registry.digitalocean.com/<REGISTRY_NAME>/openclaw-proxy:latest"}},
			{Args: []string{"docker", "push", "registry.digitalocean.com/<REGISTRY_NAME>/openclaw-proxy:latest"}},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"openclaw-909988-proxy","image":{"registry":"<REGISTRY_NAME>","repository":"openclaw-909988-proxy","tag":"latest"}}]}`}},
		},
	}

	changes := fixOpenClawDOAppPlatformProxy(plan)
	if changes == 0 {
		t.Fatal("expected proxy flow normalization to change the plan")
	}
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && cmd.Args[0] == "docker" && cmd.Args[1] == "tag" {
			t.Fatalf("expected docker tag command to be removed, got %#v", cmd.Args)
		}
	}
	foundBuild := false
	foundPush := false
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && cmd.Args[0] == "docker" && cmd.Args[1] == "build" {
			foundBuild = true
			want := "registry.digitalocean.com/<REGISTRY_NAME>/https-proxy:latest"
			if len(cmd.Args) < 7 || cmd.Args[5] != want {
				t.Fatalf("expected normalized docker build tag %q, got %#v", want, cmd.Args)
			}
		}
		if len(cmd.Args) >= 2 && cmd.Args[0] == "docker" && cmd.Args[1] == "push" {
			foundPush = true
			want := "registry.digitalocean.com/<REGISTRY_NAME>/https-proxy:latest"
			if len(cmd.Args) < 3 || cmd.Args[2] != want {
				t.Fatalf("expected normalized docker push target %q, got %#v", want, cmd.Args)
			}
		}
	}
	if !foundBuild || !foundPush {
		t.Fatalf("expected normalized build and push commands, foundBuild=%v foundPush=%v", foundBuild, foundPush)
	}
}

func TestFixOpenClawDOAppPlatformProxyNormalizesRepositoryWithoutRegistryPrefix(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Commands: []maker.Command{
			{Args: []string{"compute", "droplet", "create", "openclaw", "--region", "nyc1"}},
			{Args: []string{"registry", "create", "openclaw-registry"}, Produces: map[string]string{"REGISTRY_NAME": "name"}},
			{Args: []string{"docker", "build", "-t", "registry.digitalocean.com/<REGISTRY_NAME>/<REGISTRY_NAME>/openclaw-proxy:latest", "__CLANKER_OPENCLAW_DO_PROXY__"}},
			{Args: []string{"docker", "push", "registry.digitalocean.com/<REGISTRY_NAME>/<REGISTRY_NAME>/openclaw-proxy:latest"}},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"proxy","image":{"registry":"<REGISTRY_NAME>","repository":"<REGISTRY_NAME>/openclaw-proxy","tag":"latest"}}]}`}},
		},
	}

	changes := fixOpenClawDOAppPlatformProxy(plan)
	if changes == 0 {
		t.Fatal("expected proxy flow normalization to change the plan")
	}

	var spec map[string]any
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && cmd.Args[0] == "apps" && cmd.Args[1] == "create" {
			if err := json.Unmarshal([]byte(cmd.Args[3]), &spec); err != nil {
				t.Fatalf("unmarshal normalized spec: %v", err)
			}
		}
	}
	services := spec["services"].([]any)
	service := services[0].(map[string]any)
	image := service["image"].(map[string]any)
	if got := image["repository"].(string); got != "https-proxy" {
		t.Fatalf("expected normalized repository name, got %q", got)
	}

	wantImageRef := "registry.digitalocean.com/<REGISTRY_NAME>/https-proxy:latest"
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && cmd.Args[0] == "docker" && (cmd.Args[1] == "build" || cmd.Args[1] == "push") {
			got := cmd.Args[len(cmd.Args)-2]
			if cmd.Args[1] == "push" {
				got = cmd.Args[2]
			}
			if got != wantImageRef {
				t.Fatalf("expected normalized image ref %q, got %#v", wantImageRef, cmd.Args)
			}
		}
	}
}

func TestApplyOpenClawPlanAutofixAddsDOHTTPSOriginNote(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Commands: []maker.Command{
			{Args: []string{"compute", "droplet", "create", "openclaw", "--region", "nyc1"}},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"proxy","image":{"registry":"<REGISTRY_NAME>","repository":"https-proxy","tag":"latest"}}]}`}},
		},
	}

	patched := ApplyOpenClawPlanAutofix(plan, &RepoProfile{RepoURL: "https://github.com/openclaw/openclaw", Summary: "openclaw"}, &DeepAnalysis{}, nil)
	if patched == nil {
		t.Fatal("expected patched plan")
	}
	joined := strings.Join(patched.Notes, "\n")
	if !strings.Contains(joined, "allowedOrigins") || !strings.Contains(joined, "automatically over SSH") {
		t.Fatalf("expected allowedOrigins HTTPS patch note, got %q", joined)
	}
}

func TestApplyOpenClawPlanAutofixNormalizesBindSettingNarrative(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Notes: []string{
			"OPENCLAW_GATEWAY_BIND=0.0.0.0 ensures the gateway binds to all interfaces, allowing external connections from App Platform proxy",
		},
		Commands: []maker.Command{
			{Args: []string{"compute", "droplet", "create", "openclaw", "--region", "nyc1"}, Reason: "Create droplet and write OPENCLAW_GATEWAY_BIND=0.0.0.0 for external connections from App Platform"},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"proxy","image":{"registry":"<REGISTRY_NAME>","repository":"https-proxy","tag":"latest"}}]}`}},
		},
	}

	patched := ApplyOpenClawPlanAutofix(plan, &RepoProfile{RepoURL: "https://github.com/openclaw/openclaw", Summary: "openclaw"}, &DeepAnalysis{}, nil)
	if patched == nil {
		t.Fatal("expected patched plan")
	}
	joined := strings.Join(patched.Notes, "\n")
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatalf("expected contradictory bind-setting note to be removed, got %q", joined)
	}
	if !strings.Contains(joined, "OPENCLAW_GATEWAY_BIND=lan") {
		t.Fatalf("expected canonical bind-setting note, got %q", joined)
	}
	if !strings.Contains(patched.Commands[0].Reason, "OPENCLAW_GATEWAY_BIND=lan") {
		t.Fatalf("expected droplet reason to be normalized, got %q", patched.Commands[0].Reason)
	}
}
