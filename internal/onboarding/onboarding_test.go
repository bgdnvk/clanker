package onboarding

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScanIncludesOfficialAuthGuides(t *testing.T) {
	result := Scan(context.Background(), ScanOptions{WantedProviders: []string{"aws", "gcp", "azure", "oracle", "railway", "supabase", "flyio", "tencent", "verda", "sentry", "linear", "notion"}})

	for _, id := range []string{"aws", "gcp", "azure", "oracle", "railway", "supabase", "flyio", "tencent", "verda", "sentry", "linear", "notion"} {
		guide, ok := result.AuthGuides[id]
		if !ok {
			t.Fatalf("missing auth guide for %s", id)
		}
		if strings.TrimSpace(guide.DocsURL) == "" {
			t.Fatalf("%s auth guide missing docs URL", id)
		}
		if len(guide.LoginCommands) == 0 && strings.TrimSpace(guide.TokenURL) == "" {
			t.Fatalf("%s auth guide missing login commands and token URL", id)
		}
	}

	if !strings.Contains(result.AgentInstructions, "official docs and token URLs") {
		t.Fatalf("agent instructions do not enforce official auth sources:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "clanker_cloud_install_setup_dependencies") {
		t.Fatalf("agent instructions do not tell MCP agents to perform dependency install:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "wait for it before chat") {
		t.Fatalf("agent instructions do not require waiting for scan before app use:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "clanker_k8s_ask_cluster") {
		t.Fatalf("agent instructions do not tell MCP agents how to chat with Kubernetes clusters:\n%s", result.AgentInstructions)
	}
}

func TestGuidesPreferOfficialVendorDocs(t *testing.T) {
	guides := Guides()

	checks := map[string]string{
		"aws":        "https://docs.aws.amazon.com/",
		"gcloud":     "https://docs.cloud.google.com/",
		"az":         "https://learn.microsoft.com/",
		"doctl":      "https://docs.digitalocean.com/",
		"oci":        "https://docs.oracle.com/",
		"railway":    "https://docs.railway.com/",
		"supabase":   "https://supabase.com/docs/",
		"flyctl":     "https://fly.io/docs/",
		"tccli":      "https://www.tencentcloud.com/",
		"sentry-cli": "https://docs.sentry.io/",
	}
	for id, prefix := range checks {
		guide, ok := guides[id]
		if !ok {
			t.Fatalf("missing tool guide for %s", id)
		}
		if !strings.HasPrefix(guide.DocsURL, prefix) {
			t.Fatalf("%s docs URL = %q, want prefix %q", id, guide.DocsURL, prefix)
		}
	}
}

func TestProviderGuidesRequireOfficialTencentAndSentryCLIs(t *testing.T) {
	found := map[string][]string{}
	for _, guide := range providerGuides() {
		found[guide.ID] = guide.RequiredTools
	}
	for id, want := range map[string]string{"oracle": "oci", "tencent": "tccli", "sentry": "sentry-cli"} {
		tools := found[id]
		if len(tools) != 1 || tools[0] != want {
			t.Fatalf("%s required tools = %#v, want [%s]", id, tools, want)
		}
	}
	if normalizeToolID("tencent cloud") != "tccli" {
		t.Fatal("tencent cloud alias did not normalize to tccli")
	}
	if normalizeToolID("oracle cloud infrastructure") != "oci" {
		t.Fatal("oracle cloud infrastructure alias did not normalize to oci")
	}
	if normalizeToolID("sentry") != "sentry-cli" {
		t.Fatal("sentry alias did not normalize to sentry-cli")
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestVercelAndWranglerPathsIncludePlatformConfigDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WRANGLER_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	if fileExistsAny(vercelAuthPaths()...) {
		t.Fatal("expected no vercel auth in fresh home")
	}
	if fileExistsAny(wranglerConfigPaths()...) {
		t.Fatal("expected no wrangler config in fresh home")
	}

	var vercelAuth, wranglerConfig string
	switch runtime.GOOS {
	case "darwin":
		vercelAuth = filepath.Join(home, "Library", "Application Support", "com.vercel.cli", "auth.json")
		wranglerConfig = filepath.Join(home, "Library", "Preferences", ".wrangler", "config", "default.toml")
	case "windows":
		appData := filepath.Join(home, "AppData", "Roaming")
		t.Setenv("APPDATA", appData)
		vercelAuth = filepath.Join(appData, "com.vercel.cli", "auth.json")
		wranglerConfig = filepath.Join(appData, ".wrangler", "config", "default.toml")
	default:
		vercelAuth = filepath.Join(home, ".local", "share", "com.vercel.cli", "auth.json")
		wranglerConfig = filepath.Join(home, ".config", ".wrangler", "config", "default.toml")
	}
	writeTestFile(t, vercelAuth)
	writeTestFile(t, wranglerConfig)

	if !fileExistsAny(vercelAuthPaths()...) {
		t.Fatalf("vercel auth at %s not detected", vercelAuth)
	}
	if !fileExistsAny(wranglerConfigPaths()...) {
		t.Fatalf("wrangler config at %s not detected", wranglerConfig)
	}
}

func TestVercelAndWranglerPathsKeepLegacyLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WRANGLER_HOME", "")

	writeTestFile(t, filepath.Join(home, ".vercel", "auth.json"))
	writeTestFile(t, filepath.Join(home, ".wrangler", "config", "default.toml"))

	if !fileExistsAny(vercelAuthPaths()...) {
		t.Fatal("legacy ~/.vercel/auth.json not detected")
	}
	if !fileExistsAny(wranglerConfigPaths()...) {
		t.Fatal("legacy ~/.wrangler/config/default.toml not detected")
	}
}

func TestWranglerConfigPathsHonorWranglerHome(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WRANGLER_HOME", custom)

	writeTestFile(t, filepath.Join(custom, "config", "default.toml"))

	if !fileExistsAny(wranglerConfigPaths()...) {
		t.Fatal("config under $WRANGLER_HOME not detected")
	}
}
