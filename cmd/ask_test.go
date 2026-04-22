package cmd

import (
	"testing"

	"github.com/spf13/viper"
)

func useDefaultInfraProvider(t *testing.T, provider string) {
	t.Helper()
	previous := viper.GetString("infra.default_provider")
	viper.Set("infra.default_provider", provider)
	t.Cleanup(func() {
		viper.Set("infra.default_provider", previous)
	})
}

func useDefaultAIProvider(t *testing.T, provider string) {
	t.Helper()
	previous := viper.GetString("ai.default_provider")
	viper.Set("ai.default_provider", provider)
	t.Cleanup(func() {
		viper.Set("ai.default_provider", previous)
	})
}

func TestApplyCommandAIOverrides_DefaultsToBedrock(t *testing.T) {
	useDefaultAIProvider(t, "")

	applyCommandAIOverrides("", "", "", "", "", "", "", "", "", "", "", "", "", "")

	got := viper.GetString("ai.default_provider")
	if got != "bedrock" {
		t.Fatalf("expected default AI provider to be 'bedrock', got %q", got)
	}
}

func TestApplyCommandAIOverrides_RespectsExplicitProfile(t *testing.T) {
	useDefaultAIProvider(t, "")

	applyCommandAIOverrides("anthropic", "", "", "", "", "", "", "", "", "", "", "", "", "")

	got := viper.GetString("ai.default_provider")
	if got != "anthropic" {
		t.Fatalf("expected AI provider to be 'anthropic', got %q", got)
	}
}

func TestApplyCommandAIOverrides_RespectsConfiguredProvider(t *testing.T) {
	useDefaultAIProvider(t, "openai")

	applyCommandAIOverrides("", "", "", "", "", "", "", "", "", "", "", "", "", "")

	got := viper.GetString("ai.default_provider")
	if got != "openai" {
		t.Fatalf("expected AI provider to remain 'openai', got %q", got)
	}
}

func TestApplyDiscoveryContextDefaults_UsesConfiguredHetzner(t *testing.T) {
	useDefaultInfraProvider(t, "hetzner")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform, includeVercel, includeVerda, includeRailway := applyDiscoveryContextDefaults(false, false, false, false, false, false, false, false, false, false)

	if includeAWS {
		t.Fatal("expected discovery defaults not to force AWS when Hetzner is configured")
	}
	if includeGCP || includeAzure || includeCloudflare || includeDigitalOcean || includeVercel || includeVerda || includeRailway {
		t.Fatal("expected discovery defaults to select only the configured provider")
	}
	if !includeHetzner {
		t.Fatal("expected discovery defaults to enable Hetzner when configured")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context")
	}
}

func TestApplyDiscoveryContextDefaults_PreservesExplicitProviderSelection(t *testing.T) {
	useDefaultInfraProvider(t, "hetzner")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform, includeVercel, includeVerda, includeRailway := applyDiscoveryContextDefaults(false, false, false, false, false, true, false, false, false, false)

	if includeAWS || includeGCP || includeAzure || includeCloudflare || includeDigitalOcean || includeVercel || includeVerda || includeRailway {
		t.Fatal("expected explicit provider selection to be preserved without adding other providers")
	}
	if !includeHetzner {
		t.Fatal("expected explicit Hetzner selection to remain enabled")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context when a provider is already selected")
	}
}

func TestApplyDiscoveryContextDefaults_UsesConfiguredVercel(t *testing.T) {
	useDefaultInfraProvider(t, "vercel")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform, includeVercel, includeVerda, includeRailway := applyDiscoveryContextDefaults(false, false, false, false, false, false, false, false, false, false)

	if includeAWS {
		t.Fatal("expected discovery defaults not to force AWS when Vercel is configured")
	}
	if includeGCP || includeAzure || includeCloudflare || includeDigitalOcean || includeHetzner || includeVerda || includeRailway {
		t.Fatal("expected discovery defaults to select only the configured provider")
	}
	if !includeVercel {
		t.Fatal("expected discovery defaults to enable Vercel when configured")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context")
	}
}

func TestApplyDiscoveryContextDefaults_UsesConfiguredVerda(t *testing.T) {
	useDefaultInfraProvider(t, "verda")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform, includeVercel, includeVerda, includeRailway := applyDiscoveryContextDefaults(false, false, false, false, false, false, false, false, false, false)

	if includeAWS {
		t.Fatal("expected discovery defaults not to force AWS when Verda is configured")
	}
	if includeGCP || includeAzure || includeCloudflare || includeDigitalOcean || includeHetzner || includeVercel || includeRailway {
		t.Fatal("expected discovery defaults to select only the configured provider")
	}
	if !includeVerda {
		t.Fatal("expected discovery defaults to enable Verda when configured")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context")
	}
}

func TestApplyDiscoveryContextDefaults_UsesConfiguredRailway(t *testing.T) {
	useDefaultInfraProvider(t, "railway")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform, includeVercel, includeVerda, includeRailway := applyDiscoveryContextDefaults(false, false, false, false, false, false, false, false, false, false)

	if includeAWS {
		t.Fatal("expected discovery defaults not to force AWS when Railway is configured")
	}
	if includeGCP || includeAzure || includeCloudflare || includeDigitalOcean || includeHetzner || includeVercel || includeVerda {
		t.Fatal("expected discovery defaults to select only the configured provider")
	}
	if !includeRailway {
		t.Fatal("expected discovery defaults to enable Railway when configured")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context")
	}
}
