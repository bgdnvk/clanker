package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() {
	register("digitalocean", func() Collector { return &doCollector{} })
	register("do", func() Collector { return &doCollector{} }) // alias
}

// doCollector reads DigitalOcean App Platform logs via `doctl apps logs`.
// Resource is the app id; Service optionally selects a component. doctl emits
// plain text lines (no per-line timestamps), so entries are stamped on arrival.
type doCollector struct{}

func (c *doCollector) Provider() string { return "digitalocean" }

func (c *doCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	out, err := runJSON(ctx, "doctl", []string{"apps", "list", "--output", "json"}, opts.Env)
	if err != nil {
		return nil, err
	}
	var apps []struct {
		ID   string `json:"id"`
		Spec struct {
			Name string `json:"name"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &apps); err != nil {
		return nil, fmt.Errorf("parse do apps: %w", err)
	}
	sources := make([]Source, 0, len(apps))
	for _, a := range apps {
		sources = append(sources, Source{ID: a.ID, Kind: "app", Service: a.Spec.Name})
	}
	return sources, nil
}

func (c *doCollector) args(opts Options, follow bool) []string {
	args := []string{"apps", "logs", opts.Resource, "--type", "run"}
	if opts.Service != "" {
		args = append(args, opts.Service) // optional component name
	}
	if follow {
		args = append(args, "--follow")
	}
	return args
}

func (c *doCollector) toEntry(opts Options, line string) Entry {
	e := NewEntry("digitalocean", opts.Resource, opts.Service, line, time.Now())
	e.Service = opts.Service
	return e
}

func (c *doCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("digitalocean logs require --resource <app-id>")
	}
	out, err := runJSON(ctx, "doctl", c.args(opts, false), opts.Env)
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
		e := c.toEntry(opts, line)
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *doCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("digitalocean logs require --resource <app-id>")
	}
	matcher := newMatcher(opts)
	return streamLines(ctx, "doctl", c.args(opts, true), opts.Env, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		e := c.toEntry(opts, line)
		if !matcher.Match(e) {
			return nil
		}
		return emit(e)
	})
}
