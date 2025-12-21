package maker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

func isLambdaRoleAssumePropagationError(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "invalidparametervalueexception") &&
		strings.Contains(lower, "role") &&
		strings.Contains(lower, "cannot be assumed") &&
		strings.Contains(lower, "lambda")
}

// retryLambdaCreateFunctionOnAssumeRole retries `aws lambda create-function` when IAM propagation
// causes "role ... cannot be assumed by Lambda".
func retryLambdaCreateFunctionOnAssumeRole(ctx context.Context, awsArgs []string, stdinBytes []byte, w io.Writer) (string, error) {
	// Backoff tuned for typical IAM propagation (seconds, not minutes).
	backoffs := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}

	lastOut := ""
	lastErr := error(nil)
	for attempt, delay := range backoffs {
		select {
		case <-ctx.Done():
			return lastOut, ctx.Err()
		case <-time.After(delay):
		}

		_, _ = fmt.Fprintf(w, "[maker] note: retrying lambda create-function after IAM propagation delay (attempt=%d)\n", attempt+1)

		out, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, w)
		if err == nil {
			return out, nil
		}

		lastOut = out
		lastErr = err

		if !isLambdaRoleAssumePropagationError(out) {
			return out, err
		}
	}

	return lastOut, lastErr
}
