package logs

import (
	"context"
	"fmt"
	"time"

	"github.com/bgdnvk/clanker/internal/railway"
)

func init() { register("railway", func() Collector { return &railwayCollector{} }) }

// railwayCollector reads Railway deployment logs via the GraphQL client
// (ListDeploymentLogs returns {timestamp, message, severity}). Resource is a
// deployment id. Railway has no public tail, so Tail is poll-based.
type railwayCollector struct{}

func (c *railwayCollector) Provider() string { return "railway" }

func (c *railwayCollector) client(opts Options) (*railway.Client, error) {
	// Empty args make NewClient fall back to RAILWAY_API_TOKEN / RAILWAY_WORKSPACE_ID
	// env vars, which the caller injects from request headers.
	return railway.NewClient(opts.Env["RAILWAY_API_TOKEN"], opts.Env["RAILWAY_WORKSPACE_ID"], false)
}

func (c *railwayCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	cl, err := c.client(opts)
	if err != nil {
		return nil, err
	}
	deps, err := cl.ListDeployments(ctx, "", "", "", 50)
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(deps))
	for _, d := range deps {
		sources = append(sources, Source{ID: d.ID, Kind: "deployment", Service: d.ServiceID})
	}
	return sources, nil
}

func (c *railwayCollector) records(ctx context.Context, opts Options, limit int) ([]Entry, error) {
	if opts.Resource == "" {
		return nil, fmt.Errorf("railway logs require --resource <deployment-id>")
	}
	cl, err := c.client(opts)
	if err != nil {
		return nil, err
	}
	rows, err := cl.ListDeploymentLogs(ctx, opts.Resource, false, limit)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(rows))
	for _, r := range rows {
		msg, _ := r["message"].(string)
		ts := time.Now()
		if s, ok := r["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				ts = t
			}
		}
		e := NewEntry("railway", opts.Resource, opts.Service, msg, ts)
		e.Service = opts.Service
		if sev, ok := r["severity"].(string); ok {
			e.SetLevel(sev)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (c *railwayCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	entries, err := c.records(ctx, opts, limit)
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	for _, e := range entries {
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *railwayCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	matcher := newMatcher(opts)
	seen := newRefDedup(10 * time.Minute)
	poll := func() error {
		entries, err := c.records(ctx, opts, 200)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !seen.add(e.Ref, e.EpochMs) {
				continue
			}
			if !matcher.Match(e) {
				continue
			}
			if err := emit(e); err != nil {
				return err
			}
		}
		return nil
	}
	if err := poll(); err != nil {
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				return err
			}
		}
	}
}
