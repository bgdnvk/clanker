package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() {
	register("gcp", func() Collector { return &gcpCollector{} })
	register("gcloud", func() Collector { return &gcpCollector{} }) // alias
}

// gcpCollector reads Cloud Logging via the `gcloud logging` CLI. The generic
// Resource is a Cloud Logging filter (e.g. `resource.type="k8s_container"` or
// `logName=...`); empty reads all logs. Tail is poll-based (gcloud's native
// tail needs the alpha component).
type gcpCollector struct{}

func (c *gcpCollector) Provider() string { return "gcp" }

func (c *gcpCollector) project(opts Options) string {
	for _, k := range []string{"GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if v := strings.TrimSpace(opts.Env[k]); v != "" {
			return v
		}
	}
	return ""
}

func (c *gcpCollector) projectArgs(opts Options, base ...string) []string {
	args := append([]string{}, base...)
	if p := c.project(opts); p != "" {
		args = append(args, "--project", p)
	}
	return args
}

func (c *gcpCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	out, err := runJSON(ctx, "gcloud", c.projectArgs(opts, "logging", "logs", "list", "--format=json", "--limit=200"), opts.Env)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name string `json:"name"` // projects/<p>/logs/<id>
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse gcp logs: %w", err)
	}
	sources := make([]Source, 0, len(rows))
	for _, r := range rows {
		id := r.Name
		// Present a readable log id but keep a usable filter as the source id.
		short := id
		if i := strings.Index(id, "/logs/"); i >= 0 {
			short = id[i+len("/logs/"):]
		}
		sources = append(sources, Source{ID: fmt.Sprintf("logName=%q", id), Kind: "log", Service: short})
	}
	return sources, nil
}

type gcpEntry struct {
	Timestamp   string         `json:"timestamp"`
	Severity    string         `json:"severity"`
	TextPayload string         `json:"textPayload"`
	JSONPayload map[string]any `json:"jsonPayload"`
	LogName     string         `json:"logName"`
	InsertID    string         `json:"insertId"`
	Resource    struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Trace string `json:"trace"`
}

func (c *gcpCollector) freshness(opts Options) string {
	if opts.Since.IsZero() {
		return "30d" // count mode: look back far; --limit caps to the last N
	}
	d := time.Since(opts.Since)
	if d <= 0 {
		return "1h"
	}
	return fmt.Sprintf("%ds", int64(d.Seconds())+60)
}

func (c *gcpCollector) fetch(ctx context.Context, opts Options, limit int) ([]gcpEntry, error) {
	args := c.projectArgs(opts, "logging", "read")
	if f := strings.TrimSpace(opts.Resource); f != "" {
		args = append(args, f)
	}
	args = append(args, "--format=json", "--order=desc", fmt.Sprintf("--limit=%d", limit), "--freshness="+c.freshness(opts))
	out, err := runJSON(ctx, "gcloud", args, opts.Env)
	if err != nil {
		return nil, err
	}
	var rows []gcpEntry
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse gcp log entries: %w", err)
	}
	return rows, nil
}

func (c *gcpCollector) toEntry(opts Options, ge gcpEntry) Entry {
	ts := time.Now()
	if t, err := time.Parse(time.RFC3339Nano, ge.Timestamp); err == nil {
		ts = t
	}
	msg := ge.TextPayload
	if msg == "" && ge.JSONPayload != nil {
		if m, ok := ge.JSONPayload["message"].(string); ok && m != "" {
			msg = m
		} else if b, err := json.Marshal(ge.JSONPayload); err == nil {
			msg = string(b)
		}
	}
	// Use the unique insertId as the stream so the citation ref is unique even
	// for identical messages (avoids dedup dropping distinct entries).
	e := NewEntry("gcp", ge.LogName, ge.InsertID, msg, ts)
	if sev := strings.TrimSpace(ge.Severity); sev != "" && !strings.EqualFold(sev, "DEFAULT") {
		e.SetLevel(sev)
	}
	if ge.Resource.Type != "" {
		e.AddLabel("resourceType", ge.Resource.Type)
	}
	if ge.Trace != "" {
		e.AddLabel("trace", ge.Trace)
	}
	e.Service = opts.Service
	return e
}

func (c *gcpCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.fetch(ctx, opts, limit)
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	for _, ge := range rows {
		e := c.toEntry(opts, ge)
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *gcpCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	matcher := newMatcher(opts)
	seen := newRefDedup(10 * time.Minute)
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	poll := func(n int) error {
		rows, err := c.fetch(ctx, opts, n)
		if err != nil {
			return err
		}
		// gcloud returns newest-first; emit oldest-first so the merged view orders right.
		for i := len(rows) - 1; i >= 0; i-- {
			e := c.toEntry(opts, rows[i])
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
	if err := poll(limit); err != nil {
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
			if err := poll(100); err != nil {
				if fails++; fails >= maxConsecutivePollErrors {
					return err
				}
				EmitProgress("tail", fmt.Sprintf("gcp poll error (%d/%d), retrying: %v", fails, maxConsecutivePollErrors, err))
				continue
			}
			fails = 0
		}
	}
}
