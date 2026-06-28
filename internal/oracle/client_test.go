package oracle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadConfigProfileUsesOCIConfigFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(`[DEFAULT]
user=ocid1.user.oc1..default
fingerprint=aa:bb
key_file=/tmp/default.pem
tenancy=ocid1.tenancy.oc1..default
region=us-ashburn-1

[WORK]
user=ocid1.user.oc1..work
fingerprint=cc:dd
key_file=/tmp/work.pem
tenancy=ocid1.tenancy.oc1..work
region=eu-frankfurt-1
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OCI_CLI_CONFIG_FILE", configPath)

	cfg, ok := ReadConfigProfile("WORK")
	if !ok {
		t.Fatal("expected WORK profile")
	}
	if cfg.Name != "WORK" {
		t.Fatalf("Name = %q, want WORK", cfg.Name)
	}
	if cfg.TenancyOCID != "ocid1.tenancy.oc1..work" {
		t.Fatalf("TenancyOCID = %q", cfg.TenancyOCID)
	}
	if cfg.Region != "eu-frankfurt-1" {
		t.Fatalf("Region = %q", cfg.Region)
	}

	profiles := ConfigProfiles()
	if len(profiles) != 2 {
		t.Fatalf("profiles len = %d, want 2: %+v", len(profiles), profiles)
	}
	if profiles[0].Name != "DEFAULT" || profiles[1].Name != "WORK" {
		t.Fatalf("profiles sorted names = %+v", profiles)
	}
}
