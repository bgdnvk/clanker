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

func TestApplyDiscoveryContextDefaults_UsesConfiguredHetzner(t *testing.T) {
	useDefaultInfraProvider(t, "hetzner")

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform := applyDiscoveryContextDefaults(false, false, false, false, false, false, false)

	if includeAWS {
		t.Fatal("expected discovery defaults not to force AWS when Hetzner is configured")
	}
	if includeGCP || includeAzure || includeCloudflare || includeDigitalOcean {
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

	includeAWS, includeGCP, includeAzure, includeCloudflare, includeDigitalOcean, includeHetzner, includeTerraform := applyDiscoveryContextDefaults(false, false, false, false, false, true, false)

	if includeAWS || includeGCP || includeAzure || includeCloudflare || includeDigitalOcean {
		t.Fatal("expected explicit provider selection to be preserved without adding other providers")
	}
	if !includeHetzner {
		t.Fatal("expected explicit Hetzner selection to remain enabled")
	}
	if !includeTerraform {
		t.Fatal("expected discovery defaults to enable Terraform context when a provider is already selected")
	}
}
