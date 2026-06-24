package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	gcplogging "google.golang.org/api/logging/v2"
)

func init() {
	register("gcp", func() Collector { return &gcpCollector{} })
	register("gcloud", func() Collector { return &gcpCollector{} }) // alias
}

// gcpCollector reads Cloud Logging through Google's API client. The generic
// Resource is a Cloud Logging filter (e.g. `resource.type="k8s_container"` or
// `logName=...`); empty reads all logs.
type gcpCollector struct{}

func (c *gcpCollector) Provider() string { return "gcp" }

func (c *gcpCollector) project(opts Options) string {
	return opts.EnvValue("GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT")
}

func (c *gcpCollector) client(ctx context.Context, opts Options) (*gcplogging.Service, string, error) {
	project := c.project(opts)
	if project == "" {
		return nil, "", fmt.Errorf("gcp project is required; set GCP_PROJECT_ID or GOOGLE_CLOUD_PROJECT")
	}
	client, err := gcplogging.NewService(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("open gcp logging client for project %s: %w", project, err)
	}
	return client, project, nil
}

func (c *gcpCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	client, project, err := c.client(ctx, opts)
	if err != nil {
		return nil, err
	}

	const maxSources = 200
	sources := make([]Source, 0, maxSources)
	parent := "projects/" + project
	resp, err := client.Projects.Logs.List(parent).PageSize(maxSources).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("list gcp logs: %w", err)
	}
	for _, name := range resp.LogNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		sources = append(sources, Source{
			ID:      fmt.Sprintf("logName=%q", name),
			Kind:    "log",
			Service: gcpLogIDFromName(name),
		})
	}
	return sources, nil
}

func gcpLogIDFromName(name string) string {
	logID := strings.TrimSpace(name)
	if i := strings.Index(logID, "/logs/"); i >= 0 {
		logID = logID[i+len("/logs/"):]
	}
	if decoded, err := url.PathUnescape(logID); err == nil {
		logID = decoded
	}
	return logID
}

func gcpSeverityFloor(level string) string {
	switch normalizeLevel(level) {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARNING"
	case LevelError:
		return "ERROR"
	case LevelFatal:
		return "CRITICAL"
	default:
		return ""
	}
}

func gcpEntryFilter(opts Options, includeSince bool, extra ...string) string {
	parts := make([]string, 0, 5+len(extra))
	if resource := strings.TrimSpace(opts.Resource); resource != "" {
		parts = append(parts, "("+resource+")")
	}
	if includeSince && !opts.Since.IsZero() {
		parts = append(parts, fmt.Sprintf(`timestamp >= "%s"`, opts.Since.UTC().Format(time.RFC3339Nano)))
	}
	if !opts.Until.IsZero() {
		parts = append(parts, fmt.Sprintf(`timestamp <= "%s"`, opts.Until.UTC().Format(time.RFC3339Nano)))
	}
	if severity := gcpSeverityFloor(opts.Level); severity != "" {
		parts = append(parts, "severity >= "+severity)
	}
	for _, part := range extra {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " AND ")
}

func (c *gcpCollector) read(ctx context.Context, client *gcplogging.Service, project, filter, order string, limit int) ([]*gcplogging.LogEntry, error) {
	if limit <= 0 {
		limit = 1000
	}
	req := &gcplogging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		PageSize:      int64(limit),
	}
	if filter := strings.TrimSpace(filter); filter != "" {
		req.Filter = filter
	}
	if strings.EqualFold(strings.TrimSpace(order), "desc") {
		req.OrderBy = "timestamp desc"
	} else {
		req.OrderBy = "timestamp asc"
	}
	rows := make([]*gcplogging.LogEntry, 0, limit)
	for len(rows) < limit {
		resp, err := client.Entries.List(req).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("read gcp log entries: %w", err)
		}
		for _, entry := range resp.Entries {
			rows = append(rows, entry)
			if len(rows) >= limit {
				break
			}
		}
		if len(rows) >= limit || strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		req.PageToken = resp.NextPageToken
	}
	return rows, nil
}

func (c *gcpCollector) toEntry(opts Options, ge *gcplogging.LogEntry) Entry {
	ts := time.Now()
	if parsed, err := time.Parse(time.RFC3339Nano, ge.Timestamp); err == nil {
		ts = parsed
	} else if parsed, err := time.Parse(time.RFC3339, ge.Timestamp); err == nil {
		ts = parsed
	} else if strings.TrimSpace(ge.Timestamp) == "" {
		ts = time.Now()
	}
	msg := gcpPayloadMessage(ge.TextPayload, ge.JsonPayload, ge.ProtoPayload)
	if msg == "" && ge.HttpRequest != nil {
		msg = strings.TrimSpace(fmt.Sprintf("%s %s %d %s", ge.HttpRequest.RequestMethod, ge.HttpRequest.RequestUrl, ge.HttpRequest.Status, ge.HttpRequest.Latency))
	}
	if msg == "" {
		msg = ge.InsertId
	}
	// Use the unique insertId as the stream so the citation ref is unique even
	// for identical messages (avoids dedup dropping distinct entries).
	e := NewEntry("gcp", ge.LogName, ge.InsertId, msg, ts)
	if sev := strings.TrimSpace(ge.Severity); sev != "" && !strings.EqualFold(sev, "DEFAULT") {
		e.SetLevel(sev)
	}
	if ge.Resource != nil && ge.Resource.Type != "" {
		e.AddLabel("resourceType", ge.Resource.Type)
		for _, key := range []string{"project_id", "location", "zone", "region", "service_name", "revision_name", "cluster_name", "namespace_name", "pod_name", "container_name", "function_name", "job_name"} {
			if value := strings.TrimSpace(ge.Resource.Labels[key]); value != "" {
				e.AddLabel("resource."+key, value)
				if e.Service == "" && strings.HasSuffix(key, "_name") {
					e.Service = value
				}
			}
		}
	}
	if ge.Trace != "" {
		e.AddLabel("trace", ge.Trace)
	}
	if e.Service == "" {
		e.Service = opts.Service
	}
	return e
}

func gcpPayloadMessage(text string, jsonPayload []byte, protoPayload []byte) string {
	if text = strings.TrimSpace(text); text != "" {
		return text
	}
	if msg := gcpRawPayloadMessage(jsonPayload); msg != "" {
		return msg
	}
	return gcpRawPayloadMessage(protoPayload)
}

func gcpRawPayloadMessage(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, key := range []string{"message", "msg", "text", "event", "methodName", "status"} {
			if value, ok := obj[key]; ok {
				if msg := strings.TrimSpace(fmt.Sprint(value)); msg != "" {
					return msg
				}
			}
		}
		if b, err := json.Marshal(obj); err == nil {
			return string(b)
		}
	}
	return string(raw)
}

func (c *gcpCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	client, project, err := c.client(ctx, opts)
	if err != nil {
		return err
	}

	rows, err := c.read(ctx, client, project, gcpEntryFilter(opts, true), "desc", limit)
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
	client, project, err := c.client(ctx, opts)
	if err != nil {
		return err
	}

	var lastSeenMs int64
	emitRows := func(rows []*gcplogging.LogEntry) error {
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
	backfill, err := c.read(ctx, client, project, gcpEntryFilter(opts, true), "desc", limit)
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
			tsFilter := fmt.Sprintf("timestamp>%q", time.UnixMilli(lastSeenMs).UTC().Format(time.RFC3339Nano))
			rows, err := c.read(ctx, client, project, gcpEntryFilter(opts, false, tsFilter), "asc", 1000)
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
