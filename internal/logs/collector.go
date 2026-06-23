package logs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Options are the normalized filters every collector understands. Collectors
// push down what the provider API supports (time window, grep) and the caller
// applies the rest client-side via Match.
type Options struct {
	Provider string
	Resource string // log group / app / deployment / cluster context
	Service  string // logical service selector when Resource is unknown
	Region   string
	Since    time.Time
	Until    time.Time
	Level    string // minimum level filter (e.g. "warn")
	Grep     string // substring (or /regex/) message filter
	Limit    int    // hard cap for bounded queries
	Follow   bool   // tail mode
	Profile  string // AWS profile (also set via Env AWS_PROFILE)
	// Env carries per-provider credentials/config to inject into any CLI the
	// collector shells out to (AWS_PROFILE, GOOGLE_CLOUD_PROJECT, fly/vercel
	// tokens, etc.). Populated by the caller from request headers.
	Env map[string]string
}

// Source is a discoverable log source for a provider (a log group, app, etc.).
type Source struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Region  string `json:"region,omitempty"`
}

// Emit is the callback collectors push normalized entries to. Returning an
// error stops collection (e.g. the consumer disconnected).
type Emit func(Entry) error

// Collector pulls logs for one provider. Query is bounded backfill; Tail
// follows until ctx is cancelled. Both stream via Emit so neither buffers an
// unbounded result set.
type Collector interface {
	Provider() string
	Sources(ctx context.Context, opts Options) ([]Source, error)
	Query(ctx context.Context, opts Options, emit Emit) error
	Tail(ctx context.Context, opts Options, emit Emit) error
}

// registry maps provider id -> collector factory. Adding a new provider is a
// single Register call from its file's init() — the extension point for
// GCP/Azure/Cloudflare.
var registry = map[string]func() Collector{}

func register(provider string, factory func() Collector) {
	registry[provider] = factory
}

// Get returns the collector for a provider id, or an error if unsupported.
func Get(provider string) (Collector, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	factory, ok := registry[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported log provider %q (supported: %s)", provider, strings.Join(Providers(), ", "))
	}
	return factory(), nil
}

// Providers lists registered provider ids, sorted.
func Providers() []string {
	out := make([]string, 0, len(registry))
	for p := range registry {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// compiledMatcher precompiles the level/grep/time filter once per collection.
type compiledMatcher struct {
	level   string
	sub     string
	re      *regexp.Regexp
	sinceMs int64
	untilMs int64
}

func newMatcher(opts Options) compiledMatcher {
	m := compiledMatcher{level: strings.ToLower(strings.TrimSpace(opts.Level))}
	if !opts.Since.IsZero() {
		m.sinceMs = opts.Since.UnixMilli()
	}
	if !opts.Until.IsZero() {
		m.untilMs = opts.Until.UnixMilli()
	}
	grep := strings.TrimSpace(opts.Grep)
	if len(grep) >= 2 && strings.HasPrefix(grep, "/") && strings.HasSuffix(grep, "/") {
		if re, err := regexp.Compile("(?i)" + grep[1:len(grep)-1]); err == nil {
			m.re = re
			return m
		}
	}
	m.sub = strings.ToLower(grep)
	return m
}

// Match applies the client-side time + level + grep filter to an entry. The
// time bound makes the requested window honest for providers whose APIs don't
// (or can't) push it down (Fly/Vercel/Railway return latest-N regardless).
func (m compiledMatcher) Match(e Entry) bool {
	if m.sinceMs > 0 && e.EpochMs > 0 && e.EpochMs < m.sinceMs {
		return false
	}
	if m.untilMs > 0 && e.EpochMs > 0 && e.EpochMs > m.untilMs {
		return false
	}
	if !e.AtLeast(m.level) {
		return false
	}
	if m.re != nil {
		return m.re.MatchString(e.Message) || m.re.MatchString(e.Raw)
	}
	if m.sub != "" {
		return strings.Contains(strings.ToLower(e.Message), m.sub) ||
			strings.Contains(strings.ToLower(e.Raw), m.sub)
	}
	return true
}

// maxConsecutivePollErrors bounds transient failures before a poll-tail gives
// up, so a momentary 429/5xx/network blip doesn't end a long-running tail.
const maxConsecutivePollErrors = 5

// refDedup tracks recently-emitted entry refs for poll-based tails, keyed by
// epoch ms so it can be pruned by a sliding time window instead of cleared
// wholesale (a full clear re-emits the next poll's overlap as duplicates).
type refDedup struct {
	seen     map[string]int64
	windowMs int64
}

func newRefDedup(window time.Duration) *refDedup {
	return &refDedup{seen: map[string]int64{}, windowMs: window.Milliseconds()}
}

// add reports whether ref is new (not seen within the window) and records it.
func (d *refDedup) add(ref string, epochMs int64) bool {
	if _, ok := d.seen[ref]; ok {
		return false
	}
	d.seen[ref] = epochMs
	// Prune entries older than the retention window relative to this one.
	if len(d.seen) > 4096 {
		cutoff := epochMs - d.windowMs
		for k, ts := range d.seen {
			if ts < cutoff {
				delete(d.seen, k)
			}
		}
	}
	return true
}

// ParseSince resolves a relative ("15m", "2h", "3d") or absolute (RFC3339)
// window-start string against now. Empty defaults to 15m ago (the cost-safe
// default). now is passed in so callers/tests control the clock.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-15 * time.Minute), nil
	}
	if d, err := parseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: use 15m, 2h, 3d, or an RFC3339 timestamp", s)
}

// parseDuration extends time.ParseDuration with a "d" (day) suffix.
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(s, "%fd", &days); err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}
	return time.ParseDuration(s)
}
