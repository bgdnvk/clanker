package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultRepo = "bgdnvk/clanker"

	ChannelRelease = "release"
	ChannelMain    = "main"
)

type Options struct {
	Channel        string
	Repo           string
	InstallPath    string
	CurrentVersion string
	Token          string
	Force          bool
	DryRun         bool
	HTTPClient     *http.Client
	Stdout         io.Writer
	Stderr         io.Writer
}

type Result struct {
	Channel       string
	TargetVersion string
	TargetSHA     string
	SourceURL     string
	InstallPath   string
	Updated       bool
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubBranch struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type githubRepo struct {
	DefaultBranch string `json:"default_branch"`
}

func Update(ctx context.Context, opts Options) (Result, error) {
	channel, err := NormalizeChannel(opts.Channel)
	if err != nil {
		return Result{}, err
	}

	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = DefaultRepo
	}
	if err := validateRepo(repo); err != nil {
		return Result{}, err
	}

	installPath := strings.TrimSpace(opts.InstallPath)
	if installPath == "" {
		installPath, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("determine current executable path: %w", err)
		}
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}
	errOut := opts.Stderr
	if errOut == nil {
		errOut = io.Discard
	}

	switch channel {
	case ChannelRelease:
		return updateFromLatestRelease(ctx, client, out, repo, installPath, opts)
	case ChannelMain:
		return updateFromMain(ctx, client, out, errOut, repo, installPath, opts)
	default:
		return Result{}, fmt.Errorf("unsupported update channel %q", channel)
	}
}

func updateFromLatestRelease(ctx context.Context, client *http.Client, out io.Writer, repo, installPath string, opts Options) (Result, error) {
	release, err := latestRelease(ctx, client, repo, opts.Token)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return Result{}, errors.New("latest GitHub release did not include a tag")
	}

	result := Result{
		Channel:       ChannelRelease,
		TargetVersion: release.TagName,
		InstallPath:   installPath,
	}
	if strings.TrimSpace(opts.CurrentVersion) == release.TagName && !opts.Force {
		return result, nil
	}

	asset, err := SelectAsset(release.Assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Result{}, err
	}
	result.SourceURL = asset.BrowserDownloadURL

	if opts.DryRun {
		return result, nil
	}

	fmt.Fprintf(out, "Downloading %s...\n", asset.Name)
	binary, err := downloadReleaseBinary(ctx, client, asset.BrowserDownloadURL, opts.Token)
	if err != nil {
		return Result{}, err
	}

	if err := installBinary(binary, installPath); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func updateFromMain(ctx context.Context, client *http.Client, out, errOut io.Writer, repo, installPath string, opts Options) (Result, error) {
	branch, sha, err := latestMainCommit(ctx, client, repo, opts.Token)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(sha) == "" {
		return Result{}, errors.New("GitHub main branch response did not include a commit SHA")
	}

	result := Result{
		Channel:       ChannelMain,
		TargetVersion: "main-" + shortSHA(sha),
		TargetSHA:     sha,
		SourceURL:     fmt.Sprintf("https://github.com/%s/tree/%s", repo, branch),
		InstallPath:   installPath,
	}
	if strings.TrimSpace(opts.CurrentVersion) == result.TargetVersion && !opts.Force {
		return result, nil
	}
	if opts.DryRun {
		return result, nil
	}

	fmt.Fprintf(out, "Building clanker from %s@%s...\n", repo, shortSHA(sha))
	binary, err := buildMainBinary(ctx, out, errOut, repo, sha)
	if err != nil {
		return Result{}, err
	}
	if err := installBinary(binary, installPath); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func NormalizeChannel(channel string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "", ChannelRelease, "latest", "github-release", "github-releases", "releases":
		return ChannelRelease, nil
	case ChannelMain, "master", "source", "commit", "tip":
		return ChannelMain, nil
	default:
		return "", fmt.Errorf("invalid update channel %q (expected %q or %q)", channel, ChannelRelease, ChannelMain)
	}
}

func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("clanker_%s_%s_%s.tar.gz", tag, goos, goarch)
}

func SelectAsset(assets []githubAsset, goos, goarch string) (githubAsset, error) {
	want := AssetName("*", goos, goarch)
	for _, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" || strings.TrimSpace(asset.BrowserDownloadURL) == "" {
			continue
		}
		if strings.Contains(name, "_"+goos+"_"+goarch+".tar.gz") {
			return asset, nil
		}
	}
	return githubAsset{}, fmt.Errorf("latest release does not include a %s asset", want)
}

func latestRelease(ctx context.Context, client *http.Client, repo, token string) (githubRelease, error) {
	var release githubRelease
	if err := githubJSON(ctx, client, repo, token, "releases/latest", &release); err != nil {
		return githubRelease{}, fmt.Errorf("fetch latest release: %w", err)
	}
	return release, nil
}

func latestMainCommit(ctx context.Context, client *http.Client, repo, token string) (string, string, error) {
	branchName := "main"
	var repoInfo githubRepo
	if err := githubJSON(ctx, client, repo, token, "", &repoInfo); err != nil {
		return "", "", fmt.Errorf("fetch repository metadata: %w", err)
	}
	if strings.TrimSpace(repoInfo.DefaultBranch) != "" {
		branchName = repoInfo.DefaultBranch
	}

	var branch githubBranch
	if err := githubJSON(ctx, client, repo, token, "branches/"+branchName, &branch); err != nil {
		return "", "", fmt.Errorf("fetch %s branch: %w", branchName, err)
	}
	return branchName, branch.Commit.SHA, nil
}

func githubJSON(ctx context.Context, client *http.Client, repo, token, path string, target any) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s", repo)
	if strings.TrimSpace(path) != "" {
		url += "/" + strings.TrimLeft(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "clanker-updater")
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func downloadReleaseBinary(ctx context.Context, client *http.Client, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "clanker-updater")
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("download returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return extractBinaryFromTarGz(resp.Body)
}

func extractBinaryFromTarGz(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open tarball: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tarball: %w", err)
		}
		if header == nil || header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "clanker" {
			continue
		}
		binary, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read clanker binary from tarball: %w", err)
		}
		if len(binary) == 0 {
			return nil, errors.New("release tarball contained an empty clanker binary")
		}
		return binary, nil
	}
	return nil, errors.New("release tarball did not contain a clanker binary")
}

func buildMainBinary(ctx context.Context, out, errOut io.Writer, repo, sha string) ([]byte, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("go is required to update from main: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "clanker-update-main")
	if err != nil {
		return nil, fmt.Errorf("create temp build dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	version := "main-" + shortSHA(sha)
	module := "github.com/" + repo + "@" + sha
	cmd := exec.CommandContext(ctx, "go", "install", "-ldflags", "-X github.com/bgdnvk/clanker/cmd.Version="+version, module)
	cmd.Env = append(os.Environ(), "GOBIN="+tmpDir)
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go install %s: %w", module, err)
	}

	binaryPath := filepath.Join(tmpDir, "clanker")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("read built binary: %w", err)
	}
	if len(binary) == 0 {
		return nil, errors.New("built clanker binary was empty")
	}
	return binary, nil
}

func installBinary(binary []byte, installPath string) error {
	targetInfo, err := os.Stat(installPath)
	mode := os.FileMode(0755)
	if err == nil {
		mode = targetInfo.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat install path %s: %w", installPath, err)
	}

	dir := filepath.Dir(installPath)
	tmp, err := os.CreateTemp(dir, ".clanker-update-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return fmt.Errorf("write updated binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close updated binary: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod updated binary: %w", err)
	}
	if err := os.Rename(tmpPath, installPath); err != nil {
		return fmt.Errorf("replace %s: %w", installPath, err)
	}
	return nil
}

func validateRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid GitHub repository %q (expected owner/repo)", repo)
	}
	return nil
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}
