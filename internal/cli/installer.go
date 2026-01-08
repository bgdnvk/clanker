package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Installer handles installation of CLI tools
type Installer struct {
	platform string
	arch     string
	debug    bool
}

// NewInstaller creates a new installer for the current platform
func NewInstaller(debug bool) *Installer {
	return &Installer{
		platform: GetPlatform(),
		arch:     GetArch(),
		debug:    debug,
	}
}

// InstallOptions contains options for installation
type InstallOptions struct {
	Sudo        bool   // Use sudo for installation
	InstallPath string // Where to install binaries (default: /usr/local/bin)
}

// DefaultInstallOptions returns sensible defaults
func DefaultInstallOptions() InstallOptions {
	return InstallOptions{
		Sudo:        true,
		InstallPath: "/usr/local/bin",
	}
}

// Install installs a specific dependency by name
func (i *Installer) Install(ctx context.Context, name string, opts InstallOptions) error {
	switch name {
	case "kubectl":
		return i.InstallKubectl(ctx, opts)
	case "eksctl":
		return i.InstallEksctl(ctx, opts)
	case "aws":
		return i.InstallAWSCLI(ctx, opts)
	default:
		return fmt.Errorf("unknown dependency: %s", name)
	}
}

// InstallKubectl installs kubectl
func (i *Installer) InstallKubectl(ctx context.Context, opts InstallOptions) error {
	if i.debug {
		fmt.Println("[installer] Installing kubectl...")
	}

	// Get the latest stable version
	stableVersion, err := i.getKubectlStableVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to get kubectl version: %w", err)
	}

	if i.debug {
		fmt.Printf("[installer] Latest kubectl version: %s\n", stableVersion)
	}

	// Build download URL
	var url string
	switch i.platform {
	case "linux":
		url = fmt.Sprintf("https://dl.k8s.io/release/%s/bin/linux/%s/kubectl", stableVersion, i.arch)
	case "darwin":
		url = fmt.Sprintf("https://dl.k8s.io/release/%s/bin/darwin/%s/kubectl", stableVersion, i.arch)
	default:
		return fmt.Errorf("unsupported platform: %s", i.platform)
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "kubectl-install")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	kubectlPath := filepath.Join(tmpDir, "kubectl")

	// Download kubectl
	if err := i.downloadFile(ctx, url, kubectlPath); err != nil {
		return fmt.Errorf("failed to download kubectl: %w", err)
	}

	// Make executable
	if err := os.Chmod(kubectlPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod kubectl: %w", err)
	}

	// Move to install path
	destPath := filepath.Join(opts.InstallPath, "kubectl")
	if err := i.moveFile(ctx, kubectlPath, destPath, opts.Sudo); err != nil {
		return fmt.Errorf("failed to install kubectl: %w", err)
	}

	if i.debug {
		fmt.Printf("[installer] kubectl installed to %s\n", destPath)
	}

	return nil
}

// InstallEksctl installs eksctl
func (i *Installer) InstallEksctl(ctx context.Context, opts InstallOptions) error {
	if i.debug {
		fmt.Println("[installer] Installing eksctl...")
	}

	// Build download URL
	var url string
	archName := i.arch
	if archName == "amd64" {
		archName = "amd64"
	}

	switch i.platform {
	case "linux":
		platformName := "Linux"
		if i.arch == "arm64" {
			archName = "arm64"
		}
		url = fmt.Sprintf("https://github.com/eksctl-io/eksctl/releases/latest/download/eksctl_%s_%s.tar.gz", platformName, archName)
	case "darwin":
		platformName := "Darwin"
		if i.arch == "arm64" {
			archName = "arm64"
		}
		url = fmt.Sprintf("https://github.com/eksctl-io/eksctl/releases/latest/download/eksctl_%s_%s.tar.gz", platformName, archName)
	default:
		return fmt.Errorf("unsupported platform: %s", i.platform)
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "eksctl-install")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, "eksctl.tar.gz")

	// Download eksctl tarball
	if err := i.downloadFile(ctx, url, tarPath); err != nil {
		return fmt.Errorf("failed to download eksctl: %w", err)
	}

	// Extract tarball
	if err := i.extractTarGz(ctx, tarPath, tmpDir); err != nil {
		return fmt.Errorf("failed to extract eksctl: %w", err)
	}

	eksctlPath := filepath.Join(tmpDir, "eksctl")

	// Make executable
	if err := os.Chmod(eksctlPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod eksctl: %w", err)
	}

	// Move to install path
	destPath := filepath.Join(opts.InstallPath, "eksctl")
	if err := i.moveFile(ctx, eksctlPath, destPath, opts.Sudo); err != nil {
		return fmt.Errorf("failed to install eksctl: %w", err)
	}

	if i.debug {
		fmt.Printf("[installer] eksctl installed to %s\n", destPath)
	}

	return nil
}

// InstallAWSCLI installs AWS CLI v2
func (i *Installer) InstallAWSCLI(ctx context.Context, opts InstallOptions) error {
	if i.debug {
		fmt.Println("[installer] Installing AWS CLI v2...")
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "awscli-install")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	switch i.platform {
	case "linux":
		return i.installAWSCLILinux(ctx, tmpDir, opts)
	case "darwin":
		return i.installAWSCLIDarwin(ctx, tmpDir, opts)
	default:
		return fmt.Errorf("unsupported platform: %s", i.platform)
	}
}

func (i *Installer) installAWSCLILinux(ctx context.Context, tmpDir string, opts InstallOptions) error {
	// Build download URL
	archName := "x86_64"
	if i.arch == "arm64" {
		archName = "aarch64"
	}
	url := fmt.Sprintf("https://awscli.amazonaws.com/awscli-exe-linux-%s.zip", archName)

	zipPath := filepath.Join(tmpDir, "awscliv2.zip")

	// Download AWS CLI zip
	if err := i.downloadFile(ctx, url, zipPath); err != nil {
		return fmt.Errorf("failed to download AWS CLI: %w", err)
	}

	// Extract zip
	if err := i.extractZip(ctx, zipPath, tmpDir); err != nil {
		return fmt.Errorf("failed to extract AWS CLI: %w", err)
	}

	// Run installer
	installScript := filepath.Join(tmpDir, "aws", "install")
	args := []string{installScript, "--update"}

	if opts.Sudo {
		args = append([]string{"sudo"}, args...)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if i.debug {
		fmt.Printf("[installer] Running: %s\n", strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("AWS CLI installation failed: %w, stderr: %s", err, stderr.String())
	}

	if i.debug {
		fmt.Println("[installer] AWS CLI v2 installed successfully")
	}

	return nil
}

func (i *Installer) installAWSCLIDarwin(ctx context.Context, tmpDir string, opts InstallOptions) error {
	url := "https://awscli.amazonaws.com/AWSCLIV2.pkg"
	pkgPath := filepath.Join(tmpDir, "AWSCLIV2.pkg")

	// Download AWS CLI pkg
	if err := i.downloadFile(ctx, url, pkgPath); err != nil {
		return fmt.Errorf("failed to download AWS CLI: %w", err)
	}

	// Run installer
	args := []string{"installer", "-pkg", pkgPath, "-target", "/"}
	if opts.Sudo {
		args = append([]string{"sudo"}, args...)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if i.debug {
		fmt.Printf("[installer] Running: %s\n", strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("AWS CLI installation failed: %w, stderr: %s", err, stderr.String())
	}

	if i.debug {
		fmt.Println("[installer] AWS CLI v2 installed successfully")
	}

	return nil
}

// Helper functions

func (i *Installer) getKubectlStableVersion(ctx context.Context) (string, error) {
	url := "https://dl.k8s.io/release/stable.txt"

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get stable version: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(body)), nil
}

func (i *Installer) downloadFile(ctx context.Context, url, destPath string) error {
	if i.debug {
		fmt.Printf("[installer] Downloading %s\n", url)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (i *Installer) extractTarGz(ctx context.Context, tarPath, destDir string) error {
	cmd := exec.CommandContext(ctx, "tar", "xzf", tarPath, "-C", destDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar extraction failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}

func (i *Installer) extractZip(ctx context.Context, zipPath, destDir string) error {
	cmd := exec.CommandContext(ctx, "unzip", "-q", zipPath, "-d", destDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unzip failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}

func (i *Installer) moveFile(ctx context.Context, src, dest string, useSudo bool) error {
	var cmd *exec.Cmd
	if useSudo {
		cmd = exec.CommandContext(ctx, "sudo", "mv", src, dest)
	} else {
		cmd = exec.CommandContext(ctx, "mv", src, dest)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("move failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}
