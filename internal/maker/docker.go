package maker

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// BuildAndPushDockerImage builds a Docker image locally and pushes to ECR.
// It handles ECR authentication, building the image, tagging, and pushing.
func BuildAndPushDockerImage(ctx context.Context, clonePath, ecrURI, profile, region string, w io.Writer) (string, error) {
	accountID := extractAccountFromECR(ecrURI)
	if accountID == "" {
		return "", fmt.Errorf("failed to extract account ID from ECR URI: %s", ecrURI)
	}

	// 1. Authenticate Docker to ECR
	fmt.Fprintf(w, "[docker] authenticating to ECR...\n")
	loginScript := fmt.Sprintf("aws ecr get-login-password --region %s --profile %s | docker login --username AWS --password-stdin %s.dkr.ecr.%s.amazonaws.com",
		region, profile, accountID, region)
	loginCmd := exec.CommandContext(ctx, "bash", "-c", loginScript)
	if out, err := loginCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ECR login failed: %w\nOutput: %s", err, string(out))
	}
	fmt.Fprintf(w, "[docker] ECR authentication successful\n")

	// 2. Build the Docker image
	fmt.Fprintf(w, "[docker] building image from %s...\n", clonePath)
	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", "clanker-app", clonePath)
	buildCmd.Stdout = w
	buildCmd.Stderr = w
	if err := buildCmd.Run(); err != nil {
		return "", fmt.Errorf("docker build failed: %w", err)
	}
	fmt.Fprintf(w, "[docker] build complete\n")

	// 3. Tag with ECR URI
	imageTag := ecrURI + ":latest"
	fmt.Fprintf(w, "[docker] tagging as %s...\n", imageTag)
	tagCmd := exec.CommandContext(ctx, "docker", "tag", "clanker-app:latest", imageTag)
	if out, err := tagCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker tag failed: %w\nOutput: %s", err, string(out))
	}

	// 4. Push to ECR
	fmt.Fprintf(w, "[docker] pushing to ECR...\n")
	pushCmd := exec.CommandContext(ctx, "docker", "push", imageTag)
	pushCmd.Stdout = w
	pushCmd.Stderr = w
	if err := pushCmd.Run(); err != nil {
		return "", fmt.Errorf("docker push failed: %w", err)
	}
	fmt.Fprintf(w, "[docker] push complete\n")

	return imageTag, nil
}

// HasDockerInstalled checks if Docker is available on the system.
func HasDockerInstalled() bool {
	cmd := exec.Command("docker", "version")
	return cmd.Run() == nil
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
