package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func init() {
	register("azure", func() Collector { return &azureCollector{} })
	register("az", func() Collector { return &azureCollector{} }) // alias
}

// azureCollector reads the Azure Activity Log (control-plane events) via
// `az monitor activity-log list`. Resource optionally selects a resource group.
// Application logs live in Log Analytics (KQL) and are a future addition; this
// covers the subscription/resource-group event stream. Poll-based.
type azureCollector struct{}

func (c *azureCollector) Provider() string { return "azure" }

func (c *azureCollector) baseArgs(opts Options, extra ...string) []string {
	args := append([]string{"monitor", "activity-log", "list", "-o", "json"}, extra...)
	if rg := strings.TrimSpace(opts.Resource); rg != "" {
		args = append(args, "--resource-group", rg)
	}
	if sub := opts.EnvValue("AZURE_SUBSCRIPTION_ID"); sub != "" {
		args = append(args, "--subscription", sub)
	}
	return args
}

func (c *azureCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	// Activity log is subscription/resource-group scoped; surface resource groups
	// as selectable "sources" so the UI can scope by RG.
	args := []string{"group", "list", "-o", "json"}
	if sub := opts.EnvValue("AZURE_SUBSCRIPTION_ID"); sub != "" {
		args = append(args, "--subscription", sub)
	}
	out, err := runJSON(ctx, "az", args, opts.Env)
	if err != nil {
		return nil, err
	}
	var groups []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &groups); err != nil {
		return nil, fmt.Errorf("parse azure resource groups: %w", err)
	}
	sources := make([]Source, 0, len(groups))
	for _, g := range groups {
		sources = append(sources, Source{ID: g.Name, Kind: "resource-group"})
	}
	return sources, nil
}

type azEvent struct {
	EventTimestamp string `json:"eventTimestamp"`
	EventDataID    string `json:"eventDataId"`
	CorrelationID  string `json:"correlationId"`
	Level          string `json:"level"`
	ResourceID     string `json:"resourceId"`
	OperationName  struct {
		LocalizedValue string `json:"localizedValue"`
	} `json:"operationName"`
	Status struct {
		LocalizedValue string `json:"localizedValue"`
	} `json:"status"`
}

// fetch lists activity-log events. When startTime is non-empty it requests
// events since that instant (incremental poll); otherwise it uses an --offset
// window. Results are sorted newest-first regardless of CLI default ordering.
func (c *azureCollector) fetch(ctx context.Context, opts Options, limit int, startTime string) ([]azEvent, error) {
	var extra []string
	if startTime != "" {
		extra = []string{"--start-time", startTime}
	} else {
		offset := "1h"
		if !opts.Since.IsZero() {
			if d := time.Since(opts.Since); d > 0 {
				offset = fmt.Sprintf("%dm", int64(d.Minutes())+1)
			}
		} else {
			offset = "24h" // count mode: wider lookback, capped by --max-events
		}
		extra = []string{"--offset", offset}
	}
	extra = append(extra, "--max-events", fmt.Sprintf("%d", limit))
	out, err := runJSON(ctx, "az", c.baseArgs(opts, extra...), opts.Env)
	if err != nil {
		return nil, err
	}
	var rows []azEvent
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse azure activity log: %w", err)
	}
	// Don't rely on the CLI's default ordering for "last N".
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].EventTimestamp > rows[j].EventTimestamp })
	return rows, nil
}

func (c *azureCollector) toEntry(opts Options, ev azEvent) Entry {
	ts := time.Now()
	if t, err := time.Parse(time.RFC3339Nano, ev.EventTimestamp); err == nil {
		ts = t
	}
	msg := ev.OperationName.LocalizedValue
	if ev.Status.LocalizedValue != "" {
		msg = strings.TrimSpace(msg + " — " + ev.Status.LocalizedValue)
	}
	// Use the unique event id as the stream so the citation ref is unique even
	// for same-millisecond events with identical operation+status.
	streamID := ev.EventDataID
	if streamID == "" {
		streamID = ev.CorrelationID
	}
	e := NewEntry("azure", ev.ResourceID, streamID, msg, ts)
	if ev.Level != "" {
		// Azure levels: Informational/Warning/Error/Critical → normalized.
		e.SetLevel(ev.Level)
	}
	e.Service = opts.Resource
	return e
}

func (c *azureCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.fetch(ctx, opts, limit, "")
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	for _, ev := range rows {
		e := c.toEntry(opts, ev)
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *azureCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	matcher := newMatcher(opts)
	seen := newRefDedup(15 * time.Minute)
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	var lastSeenMs int64
	emitRows := func(rows []azEvent) error {
		// rows are newest-first; emit oldest-first for correct merge ordering.
		for i := len(rows) - 1; i >= 0; i-- {
			e := c.toEntry(opts, rows[i])
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

	backfill, err := c.fetch(ctx, opts, limit, "")
	if err != nil {
		return err
	}
	if err := emitRows(backfill); err != nil {
		return err
	}
	if lastSeenMs == 0 {
		lastSeenMs = time.Now().Add(-1 * time.Minute).UnixMilli()
	}

	ticker := time.NewTicker(10 * time.Second) // activity-log is low-frequency + billed
	defer ticker.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Incremental: only events since the last seen (no full-window rescan).
			start := time.UnixMilli(lastSeenMs + 1).UTC().Format(time.RFC3339)
			rows, err := c.fetch(ctx, opts, 1000, start)
			if err != nil {
				if fails++; fails >= maxConsecutivePollErrors {
					return err
				}
				EmitProgress("tail", fmt.Sprintf("azure poll error (%d/%d), retrying: %v", fails, maxConsecutivePollErrors, err))
				continue
			}
			fails = 0
			if err := emitRows(rows); err != nil {
				return err
			}
		}
	}
}
