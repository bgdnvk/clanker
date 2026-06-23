package logs

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func init() {
	register("flyio", func() Collector { return &flyCollector{} })
	register("fly", func() Collector { return &flyCollector{} }) // alias
}

// flyCollector reads Fly.io logs via the `flyctl` CLI. Resource is the app name.
// Fly supports a live tail (default) and `--no-tail` for a bounded recent dump,
// so Query uses --no-tail and Tail follows.
type flyCollector struct{}

func (c *flyCollector) Provider() string { return "flyio" }

func (c *flyCollector) args(opts Options, follow bool) []string {
	args := []string{"logs", "--app", opts.Resource}
	if opts.Region != "" {
		args = append(args, "--region", opts.Region)
	}
	if !follow {
		args = append(args, "--no-tail")
	}
	return args
}

// parseLine handles flyctl's text format: "<rfc3339> app[instance] region msg".
func (c *flyCollector) parseLine(opts Options, line string) Entry {
	ts := time.Now()
	msg := line
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		if t, err := time.Parse(time.RFC3339Nano, line[:idx]); err == nil {
			ts = t
			msg = strings.TrimSpace(line[idx+1:])
		} else if t, err := time.Parse(time.RFC3339, line[:idx]); err == nil {
			ts = t
			msg = strings.TrimSpace(line[idx+1:])
		}
	}
	e := NewEntry("flyio", opts.Resource, opts.Region, msg, ts)
	e.Service = opts.Service
	if opts.Region != "" {
		e.AddLabel("region", opts.Region)
	}
	return e
}

func (c *flyCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("flyio logs require --resource <app-name>")
	}
	out, err := runJSON(ctx, "flyctl", c.args(opts, false), opts.Env)
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	lines := strings.Split(string(out), "\n")
	if opts.Limit > 0 && len(lines) > opts.Limit {
		lines = lines[len(lines)-opts.Limit:]
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := c.parseLine(opts, line)
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *flyCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("flyio logs require --resource <app-name>")
	}
	matcher := newMatcher(opts)
	return streamLines(ctx, "flyctl", c.args(opts, true), opts.Env, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		e := c.parseLine(opts, line)
		if !matcher.Match(e) {
			return nil
		}
		return emit(e)
	})
}

func (c *flyCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	// `flyctl apps list` output is tabular and unstable to parse; app discovery
	// is left to the dedicated `clanker fly` commands. Return empty so the UI
	// prompts for an app name rather than failing.
	return nil, nil
}
