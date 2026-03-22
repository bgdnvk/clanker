package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var repoURLInQuestionRe = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)

var dockerPlatformLineRe = regexp.MustCompile(`(?m)^\s*Platform:\s*(linux/(?:amd64|arm64))\s*$`)

var dockerBuildxDriverLineRe = regexp.MustCompile(`(?m)^\s*Driver:\s*([a-zA-Z0-9_-]+)\s*$`)

var knownDockerCLIPluginDirs = []string{filepath.Join(".docker", "cli-plugins")}

// hasBuildxAvailable checks if docker buildx plugin is installed and working
func hasBuildxAvailable(ctx context.Context) bool {
	return hasBuildxAvailableWithConfig(ctx, "")
}

func hasBuildxAvailableWithConfig(ctx context.Context, dockerConfigDir string) bool {
	version, ok := dockerBuildxVersion(ctx, dockerConfigDir)
	return ok && strings.TrimSpace(version) != ""
}

func dockerBuildxVersion(ctx context.Context, dockerConfigDir string) (string, bool) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "version")
	cmd.Env = dockerBuildxEnv(os.Environ(), dockerConfigDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	// Check output contains version info (not just docker help)
	text := strings.TrimSpace(string(out))
	if strings.Contains(text, "buildx") || strings.Contains(text, "github.com/docker/buildx") {
		return text, true
	}
	return "", false
}

// BuildAndPushDockerImage builds a Docker image locally and pushes to ECR.
// It handles ECR authentication, building the image, tagging, and pushing.
func BuildAndPushDockerImage(ctx context.Context, clonePath, ecrURI, profile, region, imageTag string, w io.Writer) (string, error) {
	return BuildAndPushDockerImageWithRequirements(ctx, clonePath, ecrURI, profile, region, []string{imageTag}, nil, w)
}

func BuildAndPushDockerImageWithTags(ctx context.Context, clonePath, ecrURI, profile, region string, imageTags []string, w io.Writer) (string, error) {
	return BuildAndPushDockerImageWithRequirements(ctx, clonePath, ecrURI, profile, region, imageTags, nil, w)
}

func BuildAndPushDockerImageWithRequirements(ctx context.Context, clonePath, ecrURI, profile, region string, imageTags []string, requiredPlatforms []string, w io.Writer) (string, error) {
	accountID := extractAccountFromECR(ecrURI)
	if accountID == "" {
		return "", fmt.Errorf("failed to extract account ID from ECR URI: %s", ecrURI)
	}

	// 1. Authenticate Docker to ECR
	if err := dockerLoginECR(ctx, accountID, profile, region, w); err != nil {
		return "", err
	}

	if len(imageTags) == 0 {
		imageTags = []string{"latest"}
	}
	// Trim + de-dupe while preserving order.
	seen := map[string]bool{}
	cleanTags := make([]string, 0, len(imageTags))
	for _, t := range imageTags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		cleanTags = append(cleanTags, t)
	}
	if len(cleanTags) == 0 {
		cleanTags = []string{"latest"}
	}

	primaryRef := ecrURI + ":" + cleanTags[0]
	tagArgs := make([]string, 0, len(cleanTags)*2)
	for _, t := range cleanTags {
		tagArgs = append(tagArgs, "-t", ecrURI+":"+t)
	}

	requiredPlatforms = normalizeDockerPlatforms(requiredPlatforms)
	useBuildx, buildxReason, err := dockerBuildNeedsBuildx(ctx, requiredPlatforms)
	if err != nil {
		return "", err
	}
	if useBuildx {
		if !hasBuildxAvailableWithConfig(ctx, "") {
			return "", fmt.Errorf("docker buildx is required for %s but is not available", buildxReason)
		}
		if err := ensureDockerBuildxReadyWithConfig(ctx, w, ""); err != nil {
			return "", fmt.Errorf("docker buildx is required for %s but is not ready: %w", buildxReason, err)
		}
	}

	buildCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()

	if useBuildx {
		// 2a. Build+push when the target platform differs from the local Docker daemon platform.
		fmt.Fprintf(w, "[docker] building image for %s from %s using buildx (%s)...\n", strings.Join(requiredPlatforms, ", "), clonePath, buildxReason)
		buildArgs := []string{
			"buildx", "build",
			"--platform", strings.Join(requiredPlatforms, ","),
			"--progress", "plain",
			"--provenance=false",
			"--sbom=false",
			"--no-cache",
		}
		buildArgs = append(buildArgs, tagArgs...)
		buildArgs = append(buildArgs, "--push", clonePath)
		buildCmd := exec.CommandContext(buildCtx, "docker", buildArgs...)
		buildCmd.Env = dockerBuildxEnv(os.Environ(), "")
		buildCmd.Stdout = w
		buildCmd.Stderr = w
		if err := buildCmd.Run(); err != nil {
			if buildCtx.Err() != nil {
				return "", fmt.Errorf("docker buildx build --push timed out after 25m")
			}
			return "", fmt.Errorf("docker buildx build --push failed: %w", err)
		}
		fmt.Fprintf(w, "[docker] push complete\n")

		// 3. Verify the registry has the expected platforms.
		if err := verifyRemoteImagePlatformsWithConfig(ctx, primaryRef, requiredPlatforms, ""); err != nil {
			return "", err
		}
	} else {
		// 2b. Fallback: regular docker build + push (single arch, native platform only)
		fmt.Fprintf(w, "[docker] buildx not available, using regular docker build (single arch)...\n")
		fmt.Fprintf(w, "[docker] building image from %s...\n", clonePath)

		// Build with all tags
		buildArgs := []string{"build", "--no-cache"}
		buildArgs = append(buildArgs, tagArgs...)
		buildArgs = append(buildArgs, clonePath)
		buildCmd := exec.CommandContext(buildCtx, "docker", buildArgs...)
		buildCmd.Stdout = w
		buildCmd.Stderr = w
		if err := buildCmd.Run(); err != nil {
			if buildCtx.Err() != nil {
				return "", fmt.Errorf("docker build timed out after 25m")
			}
			return "", fmt.Errorf("docker build failed: %w", err)
		}
		fmt.Fprintf(w, "[docker] build complete, pushing...\n")

		// Push each tag
		for _, t := range cleanTags {
			pushRef := ecrURI + ":" + t
			pushCmd := exec.CommandContext(buildCtx, "docker", "push", pushRef)
			pushCmd.Stdout = w
			pushCmd.Stderr = w
			if err := pushCmd.Run(); err != nil {
				if buildCtx.Err() != nil {
					return "", fmt.Errorf("docker push timed out")
				}
				return "", fmt.Errorf("docker push %s failed: %w", pushRef, err)
			}
			fmt.Fprintf(w, "[docker] pushed %s\n", pushRef)
		}
		fmt.Fprintf(w, "[docker] push complete (single arch)\n")
	}

	return primaryRef, nil
}

func ensureECRTagExistsFromTag(ctx context.Context, ecrURI, profile, region, srcTag, dstTag string) error {
	srcTag = strings.TrimSpace(srcTag)
	dstTag = strings.TrimSpace(dstTag)
	if srcTag == "" || dstTag == "" {
		return fmt.Errorf("missing src/dst tag")
	}
	if strings.EqualFold(srcTag, dstTag) {
		return nil
	}
	exists, err := ecrImageTagExists(ctx, ecrURI, profile, region, dstTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	repo := extractRepositoryFromECR(ecrURI)
	if repo == "" {
		return fmt.Errorf("failed to extract repository from ECR URI: %s", ecrURI)
	}

	// Fetch the manifest for srcTag and write it to a temp file so we don't blow argv limits.
	getArgs := []string{
		"ecr", "batch-get-image",
		"--repository-name", repo,
		"--image-ids", "imageTag=" + srcTag,
		"--query", "images[0].imageManifest",
		"--output", "text",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}
	getCmd := exec.CommandContext(ctx, "aws", getArgs...)
	out, err := getCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("batch-get-image failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	manifest := strings.TrimSpace(string(out))
	if manifest == "" || strings.EqualFold(manifest, "none") {
		return fmt.Errorf("batch-get-image returned empty manifest for %s:%s", repo, srcTag)
	}

	f, err := os.CreateTemp("", "clanker-ecr-manifest-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, werr := f.WriteString(manifest); werr != nil {
		_ = f.Close()
		return werr
	}
	_ = f.Close()

	putArgs := []string{
		"ecr", "put-image",
		"--repository-name", repo,
		"--image-tag", dstTag,
		"--image-manifest", "file://" + path,
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}
	putCmd := exec.CommandContext(ctx, "aws", putArgs...)
	putOut, putErr := putCmd.CombinedOutput()
	if putErr != nil {
		lower := strings.ToLower(string(putOut))
		// If another concurrent deploy already created the tag, that's fine.
		if strings.Contains(lower, "imagealreadyexistsexception") {
			return nil
		}
		return fmt.Errorf("put-image failed: %w (%s)", putErr, strings.TrimSpace(string(putOut)))
	}

	return nil
}

func dockerLoginECR(ctx context.Context, accountID, profile, region string, w io.Writer) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("missing account ID for ECR login")
	}
	profile = strings.TrimSpace(profile)
	region = strings.TrimSpace(region)
	if profile == "" || region == "" {
		return fmt.Errorf("missing AWS profile/region for ECR login")
	}

	fmt.Fprintf(w, "[docker] authenticating to ECR...\n")
	loginScript := fmt.Sprintf(
		"aws ecr get-login-password --region %s --profile %s | docker login --username AWS --password-stdin %s.dkr.ecr.%s.amazonaws.com",
		region, profile, accountID, region,
	)
	loginCmd := exec.CommandContext(ctx, "bash", "-c", loginScript)
	if out, err := loginCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ECR login failed: %w\nOutput: %s", err, string(out))
	}
	fmt.Fprintf(w, "[docker] ECR authentication successful\n")
	return nil
}

func ensureDockerBuildxReady(ctx context.Context, w io.Writer) error {
	return ensureDockerBuildxReadyWithConfig(ctx, w, "")
}

func ensureDockerBuildxReadyWithConfig(ctx context.Context, w io.Writer, dockerConfigDir string) error {
	env := dockerBuildxEnv(os.Environ(), dockerConfigDir)
	// Ensure a docker-container builder exists/selected.
	// The Docker Desktop 'docker' driver can hang during export/unpack; docker-container avoids that.
	name := "clanker-builder"

	// Helper to select an existing builder
	selectBuilder := func() error {
		use := exec.CommandContext(ctx, "docker", "buildx", "use", name)
		use.Env = env
		return use.Run()
	}

	// Helper to create and select builder using separate commands for compatibility with all Docker versions.
	// The --use flag combined with create is not supported in older Docker buildx versions.
	createAndSelectBuilder := func() error {
		// First, clean up any existing broken builder
		rm := exec.CommandContext(ctx, "docker", "buildx", "rm", "-f", name)
		rm.Env = env
		_ = rm.Run()

		// Newer buildx versions expect the builder name via --name. Older versions may only
		// accept the positional form, so try --name first and fall back if needed.
		createArgs := [][]string{
			{"buildx", "create", "--name", name, "--driver", "docker-container"},
			{"buildx", "create", name, "--driver", "docker-container"},
		}
		var lastOut string
		var lastErr error
		created := false
		for idx, args := range createArgs {
			create := exec.CommandContext(ctx, "docker", args...)
			create.Env = env
			out, err := create.CombinedOutput()
			if err == nil {
				created = true
				break
			}
			lastErr = err
			lastOut = strings.TrimSpace(string(out))
			if idx == 0 && !strings.Contains(strings.ToLower(lastOut), "unknown flag: --name") {
				return fmt.Errorf("failed to create builder: %w\nOutput: %s", err, lastOut)
			}
		}
		if !created {
			return fmt.Errorf("failed to create builder: %w\nOutput: %s", lastErr, lastOut)
		}

		// Select the builder in a separate command (compatible with all Docker versions)
		use := exec.CommandContext(ctx, "docker", "buildx", "use", name)
		use.Env = env
		if useOut, useErr := use.CombinedOutput(); useErr != nil {
			return fmt.Errorf("failed to select builder: %w\nOutput: %s", useErr, strings.TrimSpace(string(useOut)))
		}

		return nil
	}

	// Check if builder already exists and is valid
	inspect := exec.CommandContext(ctx, "docker", "buildx", "inspect", name)
	inspect.Env = env
	out, err := inspect.CombinedOutput()
	if err == nil {
		driver := parseBuildxDriver(string(out))
		if driver == "docker-container" {
			// Builder exists with correct driver, just select it
			if selectErr := selectBuilder(); selectErr == nil {
				return nil
			}
			// Selection failed, recreate
			fmt.Fprintf(w, "[docker] existing builder invalid, recreating...\n")
		} else if driver != "" {
			// Wrong driver type, need to recreate
			fmt.Fprintf(w, "[docker] builder has wrong driver (%s), recreating...\n", driver)
		}
	}

	// Create new builder
	if createErr := createAndSelectBuilder(); createErr != nil {
		return fmt.Errorf("docker buildx is not ready (failed to create docker-container builder): %w", createErr)
	}

	fmt.Fprintf(w, "[docker] buildx builder ready: %s (driver=docker-container)\n", name)
	return nil
}

func parseBuildxDriver(inspectOutput string) string {
	inspectOutput = strings.TrimSpace(inspectOutput)
	if inspectOutput == "" {
		return ""
	}
	if m := dockerBuildxDriverLineRe.FindStringSubmatch(inspectOutput); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func verifyRemoteImagePlatforms(ctx context.Context, imageRef string, requiredPlatforms []string) error {
	return verifyRemoteImagePlatformsWithConfig(ctx, imageRef, requiredPlatforms, "")
}

func verifyRemoteImagePlatformsWithConfig(ctx context.Context, imageRef string, requiredPlatforms []string, dockerConfigDir string) error {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return fmt.Errorf("missing image ref for platform verification")
	}
	if len(requiredPlatforms) == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", imageRef)
	cmd.Env = dockerBuildxEnv(os.Environ(), dockerConfigDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to inspect pushed image platforms: %w\nOutput: %s", err, strings.TrimSpace(string(out)))
	}

	found := map[string]bool{}
	for _, m := range dockerPlatformLineRe.FindAllStringSubmatch(string(out), -1) {
		if len(m) >= 2 {
			found[strings.TrimSpace(m[1])] = true
		}
	}

	missing := make([]string, 0, len(requiredPlatforms))
	for _, p := range requiredPlatforms {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !found[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("pushed image is missing required platform(s): %s (image=%s)", strings.Join(missing, ", "), imageRef)
	}
	return nil
}

// HasDockerInstalled checks if Docker is available on the system.
func HasDockerInstalled() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func dockerDaemonAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	lower := strings.ToLower(string(out))
	if strings.Contains(lower, "cannot connect to the docker daemon") || strings.Contains(lower, "is the docker daemon running") {
		return false
	}
	// Unknown failure: assume unavailable so callers can give a deterministic message.
	return false
}

// DockerDaemonAvailableForCLI reports whether a local Docker daemon is reachable.
// This is used by CLI flows to surface a clearer message than "docker not installed".
func DockerDaemonAvailableForCLI(ctx context.Context) bool {
	return dockerDaemonAvailable(ctx)
}

func dockerBuildNeedsBuildx(ctx context.Context, requiredPlatforms []string) (bool, string, error) {
	requiredPlatforms = normalizeDockerPlatforms(requiredPlatforms)
	if len(requiredPlatforms) == 0 {
		return false, "", nil
	}
	if len(requiredPlatforms) > 1 {
		return true, "multiple target platforms", nil
	}
	nativePlatform, err := dockerDaemonPlatform(ctx)
	if err != nil {
		return true, "cross-platform target with unknown local Docker platform", nil
	}
	if nativePlatform == "" {
		return true, "cross-platform target with unknown local Docker platform", nil
	}
	if !strings.EqualFold(requiredPlatforms[0], nativePlatform) {
		return true, fmt.Sprintf("target platform %s differs from local Docker platform %s", requiredPlatforms[0], nativePlatform), nil
	}
	return false, "", nil
}

func dockerDaemonPlatform(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OSType}}/{{.Architecture}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	platform := strings.TrimSpace(string(out))
	if platform == "" {
		return "", nil
	}
	return platform, nil
}

func dockerBuildxEnv(env []string, dockerConfigDir string) []string {
	resolved := resolveDockerConfigDir(dockerConfigDir)
	if strings.TrimSpace(resolved) != "" {
		_ = ensureDockerConfigPluginDirs(resolved)
	}
	return envWithDockerConfig(env, resolved)
}

func resolveDockerConfigDir(preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		return preferred
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".docker")
	}
	return strings.TrimSpace(os.Getenv("DOCKER_CONFIG"))
}

func ensureDockerConfigPluginDirs(dockerConfigDir string) error {
	dockerConfigDir = strings.TrimSpace(dockerConfigDir)
	if dockerConfigDir == "" {
		return nil
	}
	if err := os.MkdirAll(dockerConfigDir, 0700); err != nil {
		return err
	}
	configPath := filepath.Join(dockerConfigDir, "config.json")
	payload := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return fmt.Errorf("parse docker config %s: %w", configPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	existing := toStringSlice(payload["cliPluginsExtraDirs"])
	merged := append([]string{}, existing...)
	for _, dir := range discoverDockerCLIPluginDirs() {
		if !containsString(merged, dir) {
			merged = append(merged, dir)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	payload["cliPluginsExtraDirs"] = merged
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(configPath, out, 0600)
}

func discoverDockerCLIPluginDirs() []string {
	seen := map[string]bool{}
	dirs := make([]string, 0, len(knownDockerCLIPluginDirs)+8)
	home, _ := os.UserHomeDir()
	for _, dir := range dockerCLIPluginDirCandidates() {
		resolved := dir
		if strings.HasPrefix(dir, filepath.Join(".docker", "cli-plugins")) {
			if strings.TrimSpace(home) == "" {
				continue
			}
			resolved = filepath.Join(home, dir)
		}
		resolved = strings.TrimSpace(resolved)
		if resolved == "" || seen[resolved] {
			continue
		}
		if st, err := os.Stat(resolved); err == nil && st.IsDir() {
			seen[resolved] = true
			dirs = append(dirs, resolved)
		}
	}
	return dirs
}

func dockerCLIPluginDirCandidates() []string {
	candidates := append([]string{}, knownDockerCLIPluginDirs...)
	switch runtime.GOOS {
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "Docker", "cli-plugins"),
				filepath.Join(localAppData, "Programs", "Docker", "cli-plugins"),
			)
		}
		if programFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); programFiles != "" {
			candidates = append(candidates,
				filepath.Join(programFiles, "Docker", "cli-plugins"),
				filepath.Join(programFiles, "Docker", "Docker", "resources", "cli-plugins"),
			)
		}
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			candidates = append(candidates, filepath.Join(programData, "DockerDesktop", "cli-plugins"))
		}
	case "darwin":
		candidates = append(candidates,
			"/opt/homebrew/lib/docker/cli-plugins",
			"/usr/local/lib/docker/cli-plugins",
			"/usr/local/libexec/docker/cli-plugins",
			"/Applications/Docker.app/Contents/Resources/cli-plugins",
		)
	default:
		candidates = append(candidates,
			"/home/linuxbrew/.linuxbrew/lib/docker/cli-plugins",
			"/usr/local/lib/docker/cli-plugins",
			"/usr/local/libexec/docker/cli-plugins",
			"/usr/lib/docker/cli-plugins",
			"/usr/libexec/docker/cli-plugins",
		)
	}
	return candidates
}

func normalizeDockerPlatforms(platforms []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if platform == "" || seen[platform] {
			continue
		}
		seen[platform] = true
		out = append(out, platform)
	}
	return out
}

func toStringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if direct, ok := v.([]string); ok {
			return append([]string{}, direct...)
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

// extractAccountFromECR extracts the AWS account ID from an ECR URI.
// Example: "123456789012.dkr.ecr.us-east-1.amazonaws.com/app" -> "123456789012"
func extractAccountFromECR(uri string) string {
	parts := strings.Split(uri, ".")
	if len(parts) > 0 && len(parts[0]) == 12 {
		// Verify it looks like an account ID (all digits)
		for _, c := range parts[0] {
			if c < '0' || c > '9' {
				return ""
			}
		}
		return parts[0]
	}
	return ""
}

func extractRepositoryFromECR(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	parts := strings.SplitN(uri, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func ecrImageTagExists(ctx context.Context, ecrURI, profile, region, imageTag string) (bool, error) {
	repo := extractRepositoryFromECR(ecrURI)
	if repo == "" {
		return false, fmt.Errorf("failed to extract repository from ECR URI: %s", ecrURI)
	}
	if strings.TrimSpace(imageTag) == "" {
		imageTag = "latest"
	}

	args := []string{
		"ecr", "describe-images",
		"--repository-name", repo,
		"--image-ids", "imageTag=" + imageTag,
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		lower := strings.ToLower(string(out))
		if strings.Contains(lower, "imagetagdoesnotmatchdigest") || strings.Contains(lower, "imagenotfoundexception") || strings.Contains(lower, "requested image not found") {
			return false, nil
		}
		return false, fmt.Errorf("describe-images failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if strings.Contains(string(out), "imageDigest") {
		return true, nil
	}
	return false, nil
}

func extractRepoURLFromQuestion(question string) string {
	question = strings.TrimSpace(question)
	if question == "" {
		return ""
	}
	return strings.TrimSpace(repoURLInQuestionRe.FindString(question))
}

func cloneRepoForImageBuild(ctx context.Context, repoURL string) (clonePath string, cleanup func(), err error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", nil, fmt.Errorf("missing repo URL for image build")
	}

	tmpDir, err := os.MkdirTemp("", "clanker-image-build-")
	if err != nil {
		return "", nil, err
	}

	cleanup = func() {
		_ = os.RemoveAll(tmpDir)
	}

	targetDir := filepath.Join(tmpDir, "repo")
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, targetDir)
	out, cloneErr := cmd.CombinedOutput()
	if cloneErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("git clone failed: %w (%s)", cloneErr, strings.TrimSpace(string(out)))
	}

	return targetDir, cleanup, nil
}
