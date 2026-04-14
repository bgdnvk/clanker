package gcp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func FindGcloudBinary() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CLANKER_GCLOUD_PATH")); override != "" {
		if st, err := os.Stat(override); err == nil && !st.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("CLANKER_GCLOUD_PATH set but not found: %q", override)
	}

	names := []string{"gcloud"}
	if runtime.GOOS == "windows" {
		names = []string{"gcloud.cmd", "gcloud.exe", "gcloud"}
	}

	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/opt/homebrew/bin/gcloud",
			"/usr/local/bin/gcloud",
			filepath.Join(home, "google-cloud-sdk", "bin", "gcloud"),
			filepath.Join(home, "Downloads", "google-cloud-sdk", "bin", "gcloud"),
			"/Applications/google-cloud-sdk/bin/gcloud",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/gcloud",
			"/usr/local/bin/gcloud",
			"/snap/bin/gcloud",
			filepath.Join(home, "google-cloud-sdk", "bin", "gcloud"),
			"/google-cloud-sdk/bin/gcloud",
		}
	case "windows":
		programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
		programFilesX86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)"))
		if programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if programFilesX86 != "" {
			candidates = append(candidates, filepath.Join(programFilesX86, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "AppData", "Local", "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
	}

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}

	return "", errors.New("gcloud not found (install Google Cloud SDK or set CLANKER_GCLOUD_PATH)")
}
