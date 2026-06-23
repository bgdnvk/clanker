package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func init() { register("aws", func() Collector { return &awsCollector{} }) }

// awsCollector pulls CloudWatch Logs by shelling out to the `aws` CLI, which
// resolves credentials from AWS_PROFILE/env the same way the rest of the CLI
// does. Tail is poll-based (FilterLogEvents on an interval); StartLiveTail is a
// future enhancement.
type awsCollector struct{}

func (c *awsCollector) Provider() string { return "aws" }

func (c *awsCollector) awsArgs(opts Options, base ...string) []string {
	args := append([]string{}, base...)
	args = append(args, "--output", "json")
	if opts.Region != "" {
		args = append(args, "--region", opts.Region)
	}
	if opts.Profile != "" {
		args = append(args, "--profile", opts.Profile)
	}
	return args
}

func (c *awsCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	out, err := runJSON(ctx, "aws", c.awsArgs(opts, "logs", "describe-log-groups", "--max-items", "200"), opts.Env)
	if err != nil {
		return nil, err
	}
	var data struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"logGroups"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parse log groups: %w", err)
	}
	sources := make([]Source, 0, len(data.LogGroups))
	for _, g := range data.LogGroups {
		sources = append(sources, Source{ID: g.LogGroupName, Kind: "log-group", Region: opts.Region})
	}
	return sources, nil
}

type awsEvent struct {
	Timestamp     int64  `json:"timestamp"`
	Message       string `json:"message"`
	LogStreamName string `json:"logStreamName"`
}

// fetch one bounded page of events in (start, end].
func (c *awsCollector) fetch(ctx context.Context, opts Options, startMs, endMs int64, limit int) ([]awsEvent, error) {
	if opts.Resource == "" {
		return nil, fmt.Errorf("aws logs require --resource <log-group-name>")
	}
	base := []string{
		"logs", "filter-log-events",
		"--log-group-name", opts.Resource,
		"--start-time", fmt.Sprintf("%d", startMs),
		"--end-time", fmt.Sprintf("%d", endMs),
		// --limit bounds the single API page; --no-paginate stops the CLI from
		// auto-following nextToken, so the result is a hard cap of `limit`
		// events instead of the whole window. (--max-items can't be used: it
		// conflicts with --no-paginate, which some AWS CLI configs set.)
		"--limit", fmt.Sprintf("%d", limit),
		"--no-paginate",
	}
	out, err := runJSON(ctx, "aws", c.awsArgs(opts, base...), opts.Env)
	if err != nil {
		return nil, err
	}
	var data struct {
		Events []awsEvent `json:"events"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parse log events: %w", err)
	}
	return data.Events, nil
}

func (c *awsCollector) toEntry(opts Options, ev awsEvent) Entry {
	ts := time.UnixMilli(ev.Timestamp)
	e := NewEntry("aws", opts.Resource, ev.LogStreamName, ev.Message, ts)
	e.Service = opts.Service
	if opts.Region != "" {
		e.AddLabel("region", opts.Region)
	}
	return e
}

func (c *awsCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	endMs := opts.Until.UnixMilli()
	if opts.Until.IsZero() {
		endMs = time.Now().UnixMilli()
	}
	events, err := c.fetch(ctx, opts, opts.Since.UnixMilli(), endMs, limit)
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	for _, ev := range events {
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

func (c *awsCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	matcher := newMatcher(opts)
	// Backfill the requested window, then poll for new events.
	lastMs := opts.Since.UnixMilli()
	backfill, err := c.fetch(ctx, opts, lastMs, time.Now().UnixMilli(), 1000)
	if err != nil {
		return err
	}
	seen := newRefDedup(2 * time.Minute)
	flush := func(events []awsEvent) error {
		for _, ev := range events {
			if ev.Timestamp > lastMs {
				lastMs = ev.Timestamp
			}
			e := c.toEntry(opts, ev)
			if !seen.add(e.Ref, ev.Timestamp) {
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
	if err := flush(backfill); err != nil {
		return err
	}

	ticker := time.NewTicker(5 * time.Second) // poll floor for the billed FilterLogEvents API
	defer ticker.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// 1s overlap so boundary-straddling events aren't missed; dedup by ref.
			events, err := c.fetch(ctx, opts, lastMs-1000, time.Now().UnixMilli(), 1000)
			if err != nil {
				// Tolerate transient API blips; only give up after a streak.
				if fails++; fails >= maxConsecutivePollErrors {
					return err
				}
				EmitProgress("tail", fmt.Sprintf("aws poll error (%d/%d), retrying: %v", fails, maxConsecutivePollErrors, err))
				continue
			}
			fails = 0
			if err := flush(events); err != nil {
				return err
			}
		}
	}
}
