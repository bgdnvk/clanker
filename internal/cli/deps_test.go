package cli

import (
	"runtime"
	"testing"
)

func TestGetPlatform(t *testing.T) {
	platform := GetPlatform()
	if platform != runtime.GOOS {
		t.Errorf("GetPlatform() = %s, want %s", platform, runtime.GOOS)
	}
}

func TestGetArch(t *testing.T) {
	arch := GetArch()
	expected := runtime.GOARCH
	// Normalize expectations
	if expected == "amd64" || expected == "x86_64" {
		expected = "amd64"
	} else if expected == "arm64" || expected == "aarch64" {
		expected = "arm64"
	}
	if arch != expected {
		t.Errorf("GetArch() = %s, want %s", arch, expected)
	}
}

func TestDependencyChecker_CheckKubectl(t *testing.T) {
	checker := NewDependencyChecker(false)
	status := checker.CheckKubectl()

	if status.Name != "kubectl" {
		t.Errorf("CheckKubectl().Name = %s, want kubectl", status.Name)
	}

	if !status.Required {
		t.Error("CheckKubectl().Required = false, want true")
	}

	// Either installed or not, but should not panic
	t.Logf("kubectl installed: %v, version: %s", status.Installed, status.Version)
}

func TestDependencyChecker_CheckEksctl(t *testing.T) {
	checker := NewDependencyChecker(false)
	status := checker.CheckEksctl()

	if status.Name != "eksctl" {
		t.Errorf("CheckEksctl().Name = %s, want eksctl", status.Name)
	}

	if status.Required {
		t.Error("CheckEksctl().Required = true, want false (eksctl is optional)")
	}

	// Either installed or not, but should not panic
	t.Logf("eksctl installed: %v, version: %s", status.Installed, status.Version)
}

func TestDependencyChecker_CheckAWSCLI(t *testing.T) {
	checker := NewDependencyChecker(false)
	status := checker.CheckAWSCLI()

	if status.Name != "aws" {
		t.Errorf("CheckAWSCLI().Name = %s, want aws", status.Name)
	}

	if !status.Required {
		t.Error("CheckAWSCLI().Required = false, want true")
	}

	if status.MinVersion != "2.0.0" {
		t.Errorf("CheckAWSCLI().MinVersion = %s, want 2.0.0", status.MinVersion)
	}

	// Either installed or not, but should not panic
	t.Logf("aws installed: %v, version: %s, message: %s", status.Installed, status.Version, status.Message)
}

func TestDependencyChecker_CheckAll(t *testing.T) {
	checker := NewDependencyChecker(false)
	deps := checker.CheckAll()

	if len(deps) != 3 {
		t.Errorf("CheckAll() returned %d deps, want 3", len(deps))
	}

	// Verify all expected tools are present
	names := make(map[string]bool)
	for _, dep := range deps {
		names[dep.Name] = true
	}

	expected := []string{"kubectl", "eksctl", "aws"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("CheckAll() missing %s", name)
		}
	}
}

func TestDependencyChecker_CheckMissing(t *testing.T) {
	checker := NewDependencyChecker(false)
	missing := checker.CheckMissing()

	// Just verify it does not panic and returns a valid slice
	t.Logf("Missing dependencies: %d", len(missing))
	for _, dep := range missing {
		t.Logf("  - %s: %s", dep.Name, dep.Message)
	}
}

func TestNewInstaller(t *testing.T) {
	installer := NewInstaller(false)

	if installer.platform == "" {
		t.Error("NewInstaller().platform is empty")
	}

	if installer.arch == "" {
		t.Error("NewInstaller().arch is empty")
	}

	t.Logf("Installer platform: %s, arch: %s", installer.platform, installer.arch)
}

func TestDefaultInstallOptions(t *testing.T) {
	opts := DefaultInstallOptions()

	if !opts.Sudo {
		t.Error("DefaultInstallOptions().Sudo = false, want true")
	}

	if opts.InstallPath != "/usr/local/bin" {
		t.Errorf("DefaultInstallOptions().InstallPath = %s, want /usr/local/bin", opts.InstallPath)
	}
}
