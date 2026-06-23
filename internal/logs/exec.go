package logs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// runJSON runs a CLI to completion and returns stdout. Extra env vars are
// appended to the inherited environment (used to inject per-provider creds).
func runJSON(ctx context.Context, name string, args []string, env map[string]string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = mergedEnv(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), truncate(msg, 500))
	}
	return stdout.Bytes(), nil
}

// streamLines runs a long-lived CLI (e.g. `kubectl logs -f`, `flyctl logs`),
// invoking fn for each stdout line until the process exits or ctx is cancelled.
// The child is started in its own process group so cancellation kills any
// descendants (kubectl/flyctl spawn helpers) rather than orphaning them.
func streamLines(ctx context.Context, name string, args []string, env map[string]string, fn func(line string) error) error {
	cmd := exec.Command(name, args...)
	cmd.Env = mergedEnv(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	// Kill the whole process group when ctx is cancelled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := fn(scanner.Text()); err != nil {
			// Consumer gave up (e.g. broken pipe): kill the whole process group
			// and reap, so kubectl/flyctl don't outlive us as orphans.
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			_ = cmd.Wait()
			return err
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()

	// A ctx-cancelled tail (SIGKILL) is an expected stop, not a failure.
	if ctx.Err() != nil {
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return fmt.Errorf("%s: %s", name, truncate(msg, 500))
	}
	return nil
}

func mergedEnv(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		if k == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
