package maker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// VerifyDeployment checks if the deployed application is accessible.
// It polls the endpoint until it returns a successful response or times out.
func VerifyDeployment(ctx context.Context, endpoint string, maxWait time.Duration, w io.Writer) error {
	fmt.Fprintf(w, "[health] waiting for %s to become healthy...\n", endpoint)

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(maxWait)
	attempts := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		default:
		}

		attempts++
		resp, err := client.Get(endpoint)
		if err == nil {
			statusCode := resp.StatusCode
			resp.Body.Close()

			// Consider 2xx and 3xx as success
			// Also consider 4xx as success (app is running, just responding with client error)
			if statusCode >= 200 && statusCode < 500 {
				fmt.Fprintf(w, "[health] endpoint returned %d - deployment successful! (attempts: %d)\n", statusCode, attempts)
				return nil
			}
			fmt.Fprintf(w, "[health] endpoint returned %d, waiting... (attempt %d)\n", statusCode, attempts)
		} else {
			fmt.Fprintf(w, "[health] endpoint not ready: %v (attempt %d)\n", err, attempts)
		}

		// Wait before next attempt (with jitter to avoid thundering herd)
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}

	return fmt.Errorf("deployment health check failed after %v (%d attempts)", maxWait, attempts)
}

// WaitForALBHealthy waits for the ALB to have at least one healthy target.
// This is useful after registering targets to ensure they are ready.
func WaitForALBHealthy(ctx context.Context, tgARN, profile, region string, w io.Writer, maxWait time.Duration) error {
	fmt.Fprintf(w, "[health] waiting for ALB target group to have healthy targets...\n")

	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check target health using AWS CLI
		args := []string{
			"elbv2", "describe-target-health",
			"--target-group-arn", tgARN,
			"--profile", profile,
			"--region", region,
			"--query", "TargetHealthDescriptions[].TargetHealth.State",
			"--output", "text",
		}

		out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
		if err == nil && out != "" {
			if containsHealthyTarget(out) {
				fmt.Fprintf(w, "[health] target group has healthy targets\n")
				return nil
			}
			fmt.Fprintf(w, "[health] target health states: %s\n", out)
		}

		time.Sleep(15 * time.Second)
	}

	return fmt.Errorf("no healthy targets in target group after %v", maxWait)
}

func containsHealthyTarget(healthStates string) bool {
	return len(healthStates) > 0 && (healthStates == "healthy" ||
		(len(healthStates) > 7 && healthStates[:7] == "healthy"))
}
