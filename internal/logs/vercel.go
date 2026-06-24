package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/vercel"
)

func init() { register("vercel", func() Collector { return &vercelCollector{} }) }

// vercelCollector reads Vercel deployment build/runtime events via the REST API
// (/v2/deployments/<uid>/events). Resource is a deployment UID. Vercel runtime
// logs are per-deployment and short-lived, so Tail is poll-based over events.
type vercelCollector struct{}

func (c *vercelCollector) Provider() string { return "vercel" }

func (c *vercelCollector) client(opts Options) (*vercel.Client, error) {
	return vercel.NewClient(opts.Env["VERCEL_TOKEN"], opts.Env["VERCEL_TEAM_ID"], false)
}

func (c *vercelCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	cl, err := c.client(opts)
	if err != nil {
		return nil, err
	}
	body, err := cl.RunAPIWithContext(ctx, "GET", "/v6/deployments?limit=20", "")
	if err != nil {
		return nil, err
	}
	var data struct {
		Deployments []struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"deployments"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, fmt.Errorf("parse deployments: %w", err)
	}
	sources := make([]Source, 0, len(data.Deployments))
	for _, d := range data.Deployments {
		sources = append(sources, Source{ID: d.UID, Kind: "deployment", Service: d.Name})
	}
	return sources, nil
}

func (c *vercelCollector) events(ctx context.Context, opts Options, limit int) ([]Entry, error) {
	if opts.Resource == "" {
		return nil, fmt.Errorf("vercel logs require --resource <deployment-uid>")
	}
	cl, err := c.client(opts)
	if err != nil {
		return nil, err
	}
	endpoint := "/v2/deployments/" + url.PathEscape(opts.Resource) + fmt.Sprintf("/events?limit=%d", limit)
	body, err := cl.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &rows); err != nil {
		return nil, fmt.Errorf("parse events: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, r := range rows {
		msg := vercelText(r)
		if msg == "" {
			continue
		}
		ts := time.Now()
		if ms, ok := r["created"].(float64); ok {
			ts = time.UnixMilli(int64(ms))
		}
		e := NewEntry("vercel", opts.Resource, opts.Service, msg, ts)
		e.Service = opts.Service
		if typ, ok := r["type"].(string); ok {
			if typ == "stderr" || typ == "error" {
				e.SetLevel("error")
			}
			e.AddLabel("type", typ)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// vercelText extracts the log text from an event row's varied shapes.
func vercelText(r map[string]any) string {
	if t, ok := r["text"].(string); ok && t != "" {
		return t
	}
	if p, ok := r["payload"].(map[string]any); ok {
		if t, ok := p["text"].(string); ok && t != "" {
			return t
		}
	}
	return ""
}

func (c *vercelCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	entries, err := c.events(ctx, opts, limit)
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

func (c *vercelCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	matcher := newMatcher(opts)
	seen := newRefDedup(10 * time.Minute)
	poll := func() error {
		entries, err := c.events(ctx, opts, 100)
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
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				if fails++; fails >= maxConsecutivePollErrors {
					return err
				}
				EmitProgress("tail", fmt.Sprintf("vercel poll error (%d/%d), retrying: %v", fails, maxConsecutivePollErrors, err))
				continue
			}
			fails = 0
		}
	}
}
