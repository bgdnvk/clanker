package maker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var repoURLInQuestionRe = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)

var dockerPlatformLineRe = regexp.MustCompile(`(?m)^\s*Platform:\s*(linux/(?:amd64|arm64))\s*$`)

var dockerBuildxDriverLineRe = regexp.MustCompile(`(?m)^\s*Driver:\s*([a-zA-Z0-9_-]+)\s*$`)

// BuildAndPushDockerImage builds a Docker image locally and pushes to ECR.
// It handles ECR authentication, building the image, tagging, and pushing.
func BuildAndPushDockerImage(ctx context.Context, clonePath, ecrURI, profile, region, imageTag string, w io.Writer) (string, error) {
	return BuildAndPushDockerImageWithTags(ctx, clonePath, ecrURI, profile, region, []string{imageTag}, w)
}

func BuildAndPushDockerImageWithTags(ctx context.Context, clonePath, ecrURI, profile, region string, imageTags []string, w io.Writer) (string, error) {
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

	if err := ensureDockerBuildxReady(ctx, w); err != nil {
		return "", err
	}

	// 2. Build+push a multi-arch image. This avoids shipping an arm64-only image when the target is amd64 (or vice versa).
	fmt.Fprintf(w, "[docker] building multi-arch image (linux/amd64, linux/arm64) from %s...\n", clonePath)
	buildCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	buildArgs := []string{
		"buildx", "build",
		"--builder", "clanker-builder",
		"--platform", "linux/amd64,linux/arm64",
		"--progress", "plain",
		"--provenance=false",
		"--sbom=false",
		"--no-cache",
	}
	buildArgs = append(buildArgs, tagArgs...)
	buildArgs = append(buildArgs, "--push", clonePath)
	buildCmd := exec.CommandContext(buildCtx, "docker", buildArgs...)
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
	if err := verifyRemoteImagePlatforms(ctx, primaryRef, []string{"linux/amd64", "linux/arm64"}); err != nil {
		return "", err
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
	// Ensure a docker-container builder exists/selected.
	// The Docker Desktop 'docker' driver can hang during export/unpack; docker-container avoids that.
	name := "clanker-builder"

	// Inspect existing builder (if any) and check driver.
	inspect := exec.CommandContext(ctx, "docker", "buildx", "inspect", name)
	out, err := inspect.CombinedOutput()
	if err == nil {
		driver := parseBuildxDriver(string(out))
		if driver == "docker-container" {
			use := exec.CommandContext(ctx, "docker", "buildx", "use", name)
			_ = use.Run()
			return nil
		}

		// If it's the wrong driver, recreate.
		_ = exec.CommandContext(ctx, "docker", "buildx", "rm", "-f", name).Run()
	}

	create := exec.CommandContext(ctx, "docker", "buildx", "create", "--use", "--name", name, "--driver", "docker-container")
	out, err = create.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker buildx is not ready (failed to create docker-container builder): %w\nOutput: %s", err, strings.TrimSpace(string(out)))
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
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return fmt.Errorf("missing image ref for platform verification")
	}
	if len(requiredPlatforms) == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", imageRef)
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
