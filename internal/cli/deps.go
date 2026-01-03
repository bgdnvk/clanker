// Package cli provides CLI tool dependency detection and installation.
package cli

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// DependencyChecker handles detection of CLI tools
type DependencyChecker struct {
	debug bool
}

// NewDependencyChecker creates a new dependency checker
func NewDependencyChecker(debug bool) *DependencyChecker {
	return &DependencyChecker{debug: debug}
}

// DependencyStatus represents the status of a CLI tool
type DependencyStatus struct {
	Name       string
	Installed  bool
	Version    string
	Required   bool
	MinVersion string
	Message    string
}

// CheckAll checks all K8s related dependencies
func (d *DependencyChecker) CheckAll() []DependencyStatus {
	return []DependencyStatus{
		d.CheckKubectl(),
		d.CheckEksctl(),
		d.CheckAWSCLI(),
	}
}

// CheckMissing returns only the missing or invalid dependencies
func (d *DependencyChecker) CheckMissing() []DependencyStatus {
	all := d.CheckAll()
	var missing []DependencyStatus
	for _, dep := range all {
		if !dep.Installed || (dep.Message != "" && strings.Contains(dep.Message, "upgrade")) {
			missing = append(missing, dep)
		}
	}
	return missing
}

// CheckKubectl checks if kubectl is installed
func (d *DependencyChecker) CheckKubectl() DependencyStatus {
	status := DependencyStatus{
		Name:     "kubectl",
		Required: true,
	}

	path, err := exec.LookPath("kubectl")
	if err != nil {
		status.Installed = false
		status.Message = "kubectl is not installed"
		return status
	}

	status.Installed = true

	// Get version
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, path, "version", "--client", "-o", "yaml")
	output, err := cmd.Output()
	if err != nil {
		// Try without -o yaml for older versions
		cmd = exec.CommandContext(ctx, path, "version", "--client", "--short")
		output, err = cmd.Output()
	}

	if err == nil {
		status.Version = strings.TrimSpace(string(output))
		// Extract just the version number
		if re := regexp.MustCompile(`v\d+\.\d+\.\d+`); re.Match(output) {
			status.Version = re.FindString(string(output))
		}
	}

	return status
}

// CheckEksctl checks if eksctl is installed
func (d *DependencyChecker) CheckEksctl() DependencyStatus {
	status := DependencyStatus{
		Name:     "eksctl",
		Required: false, // Only required for EKS operations
	}

	path, err := exec.LookPath("eksctl")
	if err != nil {
		status.Installed = false
		status.Message = "eksctl is not installed (required for EKS cluster management)"
		return status
	}

	status.Installed = true

	// Get version
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, path, "version")
	output, err := cmd.Output()
	if err == nil {
		status.Version = strings.TrimSpace(string(output))
	}

	return status
}

// CheckAWSCLI checks if AWS CLI v2 is installed
func (d *DependencyChecker) CheckAWSCLI() DependencyStatus {
	status := DependencyStatus{
		Name:       "aws",
		Required:   true,
		MinVersion: "2.0.0",
	}

	path, err := exec.LookPath("aws")
	if err != nil {
		status.Installed = false
		status.Message = "AWS CLI is not installed"
		return status
	}

	// Get version
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, path, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		status.Installed = false
		status.Message = "failed to get AWS CLI version"
		return status
	}

	versionOutput := strings.TrimSpace(stdout.String())
	status.Version = versionOutput

	// Parse version: "aws-cli/2.15.0 Python/3.11.6 ..."
	if re := regexp.MustCompile(`aws-cli/(\d+)\.(\d+)\.(\d+)`); re.MatchString(versionOutput) {
		matches := re.FindStringSubmatch(versionOutput)
		if len(matches) >= 2 {
			majorVersion := matches[1]
			status.Version = strings.Join(matches[1:], ".")

			// Check if it's v2
			if majorVersion == "1" {
				status.Installed = true
				status.Message = "AWS CLI v1 detected; v2 is required (v1 breaks --no-cli-pager)"
				return status
			}
		}
	}

	status.Installed = true
	return status
}

// GetPlatform returns the current platform (linux, darwin)
func GetPlatform() string {
	return runtime.GOOS
}

// GetArch returns the current architecture (amd64, arm64)
func GetArch() string {
	arch := runtime.GOARCH
	// Normalize architecture names
	switch arch {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return arch
	}
}
