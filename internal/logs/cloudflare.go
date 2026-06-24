package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() {
	register("cloudflare", func() Collector { return &cfCollector{} })
	register("cf", func() Collector { return &cfCollector{} }) // alias
}

// cfCollector tails Cloudflare Worker logs via `wrangler tail`. Resource is the
// Worker (script) name. This is live-only — Workers logs aren't historically
// queryable via wrangler (Logpush is the batch path), so Query returns nothing
// and the UI's Latest/live mode (Tail) is the supported path.
type cfCollector struct{}

func (c *cfCollector) Provider() string { return "cloudflare" }

func (c *cfCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	// Worker discovery needs the account API; left to the dedicated cf commands.
	return nil, nil
}

func (c *cfCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("cloudflare logs require --resource <worker-name>")
	}
	// No historical query for Worker logs via wrangler. Surface a clear note
	// rather than a confusing empty result; use Latest/live to tail.
	EmitProgress("collect", "cloudflare Worker logs are live-only — use Latest/live to tail")
	return nil
}

// cfTailEvent is one `wrangler tail --format json` record (a Worker invocation).
type cfTailEvent struct {
	ScriptName     string `json:"scriptName"`
	Outcome        string `json:"outcome"`
	EventTimestamp int64  `json:"eventTimestamp"`
	Logs           []struct {
		Message   []any  `json:"message"`
		Level     string `json:"level"`
		Timestamp int64  `json:"timestamp"`
	} `json:"logs"`
	Exceptions []struct {
		Name      string `json:"name"`
		Message   string `json:"message"`
		Timestamp int64  `json:"timestamp"`
	} `json:"exceptions"`
	Event struct {
		Request struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
	} `json:"event"`
}

func cfMessage(parts []any) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			out = append(out, v)
		default:
			if b, err := json.Marshal(v); err == nil {
				out = append(out, string(b))
			}
		}
	}
	return strings.Join(out, " ")
}

func (c *cfCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("cloudflare logs require --resource <worker-name>")
	}
	matcher := newMatcher(opts)
	emitEntry := func(level, msg string, tsMs int64) error {
		if strings.TrimSpace(msg) == "" {
			return nil
		}
		ts := time.Now()
		if tsMs > 0 {
			ts = time.UnixMilli(tsMs)
		}
		e := NewEntry("cloudflare", opts.Resource, opts.Resource, msg, ts)
		if level != "" {
			e.SetLevel(level)
		}
		e.Service = opts.Service
		if !matcher.Match(e) {
			return nil
		}
		return emit(e)
	}

	return streamLines(ctx, "wrangler", []string{"tail", opts.Resource, "--format", "json"}, opts.Env, func(line string) error {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			return nil // skip wrangler's human banner lines
		}
		var ev cfTailEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil
		}
		for _, l := range ev.Logs {
			if err := emitEntry(l.Level, cfMessage(l.Message), l.Timestamp); err != nil {
				return err
			}
		}
		for _, ex := range ev.Exceptions {
			if err := emitEntry("error", strings.TrimSpace(ex.Name+": "+ex.Message), ex.Timestamp); err != nil {
				return err
			}
		}
		// If the invocation produced no logs/exceptions, record the request itself.
		if len(ev.Logs) == 0 && len(ev.Exceptions) == 0 {
			req := strings.TrimSpace(ev.Event.Request.Method + " " + ev.Event.Request.URL)
			lvl := "info"
			if ev.Outcome != "" && ev.Outcome != "ok" {
				lvl = "error"
			}
			if req != "" {
				if err := emitEntry(lvl, req+" ("+ev.Outcome+")", ev.EventTimestamp); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
