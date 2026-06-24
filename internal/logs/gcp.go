package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	return opts.EnvValue("GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT")
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
	// `gcloud logging logs list --format=json` returns an array of (URL-encoded)
	// log-name strings, e.g. "projects/<p>/logs/run.googleapis.com%2Fstdout".
	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		return nil, fmt.Errorf("parse gcp logs: %w", err)
	}
	sources := make([]Source, 0, len(names))
	for _, raw := range names {
		name := raw
		if d, derr := url.PathUnescape(raw); derr == nil {
			name = d
		}
		short := name
		if i := strings.Index(name, "/logs/"); i >= 0 {
			short = name[i+len("/logs/"):]
		}
		// The source id is a ready-to-use Cloud Logging filter.
		sources = append(sources, Source{ID: fmt.Sprintf("logName=%q", name), Kind: "log", Service: short})
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
	HTTPRequest struct {
		RequestMethod string `json:"requestMethod"`
		RequestURL    string `json:"requestUrl"`
		Status        int    `json:"status"`
		Latency       string `json:"latency"`
	} `json:"httpRequest"`
	ProtoPayload map[string]any `json:"protoPayload"`
	Trace        string         `json:"trace"`
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

// read runs `gcloud logging read` with an explicit filter/order/freshness.
func (c *gcpCollector) read(ctx context.Context, opts Options, filter, order string, limit int, freshness string) ([]gcpEntry, error) {
	args := c.projectArgs(opts, "logging", "read")
	if f := strings.TrimSpace(filter); f != "" {
		args = append(args, f)
	}
	args = append(args, "--format=json", "--order="+order, fmt.Sprintf("--limit=%d", limit))
	if freshness != "" {
		args = append(args, "--freshness="+freshness)
	}
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
	// Request logs (Cloud Run / API Gateway) carry no payload — synthesize from
	// httpRequest; audit logs from protoPayload.methodName.
	if msg == "" && ge.HTTPRequest.RequestMethod != "" {
		msg = strings.TrimSpace(fmt.Sprintf("%s %s %d %s",
			ge.HTTPRequest.RequestMethod, ge.HTTPRequest.RequestURL, ge.HTTPRequest.Status, ge.HTTPRequest.Latency))
	}
	if msg == "" && ge.ProtoPayload != nil {
		if mn, ok := ge.ProtoPayload["methodName"].(string); ok && mn != "" {
			msg = mn
		} else if b, err := json.Marshal(ge.ProtoPayload); err == nil {
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
	rows, err := c.read(ctx, opts, opts.Resource, "desc", limit, c.freshness(opts))
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
	var lastSeenMs int64
	emitRows := func(rows []gcpEntry) error {
		for _, ge := range rows {
			e := c.toEntry(opts, ge)
			if e.EpochMs > lastSeenMs {
				lastSeenMs = e.EpochMs
			}
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

	// Backfill: newest N (desc), emitted oldest-first.
	backfill, err := c.read(ctx, opts, opts.Resource, "desc", limit, c.freshness(opts))
	if err != nil {
		return err
	}
	for i, j := 0, len(backfill)-1; i < j; i, j = i+1, j-1 {
		backfill[i], backfill[j] = backfill[j], backfill[i]
	}
	if err := emitRows(backfill); err != nil {
		return err
	}
	if lastSeenMs == 0 {
		lastSeenMs = time.Now().Add(-1 * time.Minute).UnixMilli()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Incremental: only entries strictly newer than the last seen, asc.
			// Bounds cost (no full-window rescan) and never skips a busy burst.
			// >= (not >) so an entry exactly at the last-seen ms boundary isn't
			// skipped; refDedup drops the re-included boundary rows. freshness=1d
			// tolerates ingestion lag for backdated entries.
			tsFilter := fmt.Sprintf("timestamp>=%q", time.UnixMilli(lastSeenMs).UTC().Format(time.RFC3339Nano))
			filter := tsFilter
			if r := strings.TrimSpace(opts.Resource); r != "" {
				filter = "(" + r + ") AND " + tsFilter
			}
			rows, err := c.read(ctx, opts, filter, "asc", 1000, "1d")
			if err != nil {
				if fails++; fails >= maxConsecutivePollErrors {
					return err
				}
				EmitProgress("tail", fmt.Sprintf("gcp poll error (%d/%d), retrying: %v", fails, maxConsecutivePollErrors, err))
				continue
			}
			fails = 0
			if err := emitRows(rows); err != nil {
				return err
			}
		}
	}
}
