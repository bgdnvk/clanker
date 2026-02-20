package maker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// HealthCheckConfig specifies how to verify a Node.js app is running
type HealthCheckConfig struct {
	Host         string // hostname or IP
	Port         int    // app listening port
	HTTPEndpoint string // HTTP health endpoint if available (e.g., "/health")
	ExposesHTTP  bool   // does the app serve HTTP?
}

// VerifyNodeJSDeployment checks if a Node.js app is running and healthy.
// Strategy:
// 1. If app exposes HTTP with health endpoint, use HTTP check
// 2. If app exposes HTTP without health endpoint, check root returns non-5xx
// 3. If app doesn't expose HTTP, verify TCP port is open
func VerifyNodeJSDeployment(ctx context.Context, cfg HealthCheckConfig, maxWait time.Duration, w io.Writer) error {
	deadline := time.Now().Add(maxWait)
	attempts := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		default:
		}
		attempts++

		if cfg.ExposesHTTP {
			// Try HTTP health check
			endpoint := cfg.HTTPEndpoint
			if endpoint == "" {
				endpoint = "/"
			}
			url := fmt.Sprintf("http://%s:%d%s", cfg.Host, cfg.Port, endpoint)

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(url)
			if err == nil {
				statusCode := resp.StatusCode
				resp.Body.Close()
				if statusCode < 500 {
					fmt.Fprintf(w, "[health] HTTP %s returned %d - healthy! (attempts: %d)\n", url, statusCode, attempts)
					return nil
				}
				fmt.Fprintf(w, "[health] HTTP returned %d, waiting... (attempt %d)\n", statusCode, attempts)
			} else {
				fmt.Fprintf(w, "[health] HTTP not ready: %v (attempt %d)\n", err, attempts)
			}
		} else {
			// TCP port check for non-HTTP apps (WebSocket, workers, etc.)
			addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				conn.Close()
				fmt.Fprintf(w, "[health] TCP port %d is open - app is running! (attempts: %d)\n", cfg.Port, attempts)
				return nil
			}
			fmt.Fprintf(w, "[health] TCP port %d not ready (attempt %d)\n", cfg.Port, attempts)
		}

		// Wait before next attempt
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}

	if cfg.ExposesHTTP {
		return fmt.Errorf("HTTP health check failed after %v (%d attempts)", maxWait, attempts)
	}
	return fmt.Errorf("TCP health check failed after %v (%d attempts)", maxWait, attempts)
}

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
