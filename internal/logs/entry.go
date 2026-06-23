// Package logs provides a provider-agnostic model and collectors for the
// unified multi-provider log viewer + "talk to logs" agent. Every provider
// normalizes its native records into a single Entry so the backend can merge,
// sort, filter, and stream them as one time-ordered view, and so the chat agent
// can ground answers in real, citable lines.
package logs

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Levels, normalized across providers. Unknown is used rather than guessing
// "info" so a missing/unparseable severity is never silently treated as benign.
const (
	LevelTrace   = "trace"
	LevelDebug   = "debug"
	LevelInfo    = "info"
	LevelWarn    = "warn"
	LevelError   = "error"
	LevelFatal   = "fatal"
	LevelUnknown = "unknown"
)

// levelRank orders severities for "minimum level" filtering. Unknown sorts
// below info so a level>=warn filter doesn't accidentally hide unknown lines
// that might be errors — unknown is only dropped by an explicit info+ filter.
var levelRank = map[string]int{
	LevelTrace:   0,
	LevelDebug:   1,
	LevelUnknown: 1,
	LevelInfo:    2,
	LevelWarn:    3,
	LevelError:   4,
	LevelFatal:   5,
}

// Entry is the normalized log record shared CLI -> backend -> frontend.
type Entry struct {
	Ts         string            `json:"ts"`                   // RFC3339Nano UTC
	EpochMs    int64             `json:"epochMs"`              // sort key without reparsing
	Provider   string            `json:"provider"`             // aws|k8s|flyio|vercel|railway
	Source     string            `json:"source"`               // log group / app / deployment / cluster
	Service    string            `json:"service,omitempty"`    // logical service/resource
	Stream     string            `json:"stream,omitempty"`     // log stream / pod-container / region
	Level      string            `json:"level"`                // normalized enum
	RawLevel   string            `json:"rawLevel,omitempty"`   // provider's original severity (auditable)
	Message    string            `json:"message"`              // extracted if JSON, else == Raw
	Structured map[string]any    `json:"structured,omitempty"` // parsed JSON fields when present
	Labels     map[string]string `json:"labels,omitempty"`     // region, traceId, requestId, ...
	Raw        string            `json:"raw,omitempty"`        // original line verbatim
	Ref        string            `json:"ref"`                  // stable citation id
	Cursor     string            `json:"cursor,omitempty"`     // opaque per-source position token
}

// NewEntry builds a normalized Entry from a provider's raw line + timestamp,
// auto-detecting structured JSON and normalizing severity. provider/source are
// required; message defaults to the raw line.
func NewEntry(provider, source, stream, raw string, ts time.Time) Entry {
	ts = ts.UTC()
	e := Entry{
		Ts:       ts.Format(time.RFC3339Nano),
		EpochMs:  ts.UnixMilli(),
		Provider: provider,
		Source:   source,
		Stream:   stream,
		Raw:      raw,
		Message:  raw,
		Level:    LevelUnknown,
	}
	e.applyStructured(raw)
	if e.Level == LevelUnknown {
		if lvl := levelFromText(e.Message); lvl != "" {
			e.Level = lvl
		}
	}
	e.Ref = makeRef(provider, stream, ts, e.Message)
	return e
}

// applyStructured tries to parse the raw line as a JSON object and lift common
// fields (message, level, trace/request ids) into the normalized shape.
func (e *Entry) applyStructured(raw string) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return
	}
	e.Structured = obj

	for _, k := range []string{"message", "msg", "text", "log", "event"} {
		if v, ok := obj[k]; ok {
			if s := asString(v); s != "" {
				e.Message = s
				break
			}
		}
	}
	for _, k := range []string{"level", "severity", "lvl", "loglevel", "log_level"} {
		if v, ok := obj[k]; ok {
			if s := asString(v); s != "" {
				e.RawLevel = s
				e.Level = normalizeLevel(s)
				break
			}
		}
	}
	for _, k := range []string{"trace_id", "traceId", "traceID", "request_id", "requestId", "span_id", "spanId"} {
		if v, ok := obj[k]; ok {
			if s := asString(v); s != "" {
				if e.Labels == nil {
					e.Labels = map[string]string{}
				}
				e.Labels[k] = s
			}
		}
	}
}

// SetLevel applies a provider-native severity string, recording both the raw
// value and the normalized enum.
func (e *Entry) SetLevel(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	e.RawLevel = raw
	e.Level = normalizeLevel(raw)
}

// AddLabel attaches a provider dimension (region, account, env, ...).
func (e *Entry) AddLabel(k, v string) {
	if k == "" || v == "" {
		return
	}
	if e.Labels == nil {
		e.Labels = map[string]string{}
	}
	e.Labels[k] = v
}

// AtLeast reports whether the entry's level is >= the given minimum level.
// An empty or unrecognized minimum passes everything.
func (e Entry) AtLeast(min string) bool {
	min = strings.ToLower(strings.TrimSpace(min))
	minRank, ok := levelRank[min]
	if !ok {
		return true
	}
	return levelRank[e.Level] >= minRank
}

func normalizeLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace
	case "debug", "dbg", "fine", "finer", "finest", "verbose", "default":
		return LevelDebug
	case "info", "information", "informational", "notice", "i":
		return LevelInfo
	case "warn", "warning", "w":
		return LevelWarn
	case "error", "err", "e", "severe":
		return LevelError
	case "fatal", "critical", "crit", "panic", "alert", "emergency", "emerg":
		return LevelFatal
	default:
		return LevelUnknown
	}
}

// levelFromText is a cheap heuristic for unstructured lines with no explicit
// severity field. Returns "" when nothing matches so the caller keeps Unknown.
func levelFromText(msg string) string {
	up := strings.ToUpper(msg)
	switch {
	case strings.Contains(up, "FATAL"), strings.Contains(up, "PANIC"), strings.Contains(up, "CRITICAL"):
		return LevelFatal
	case strings.Contains(up, "ERROR"), strings.Contains(up, "ERR "), strings.Contains(up, "EXCEPTION"), strings.Contains(up, "TRACEBACK"):
		return LevelError
	case strings.Contains(up, "WARN"):
		return LevelWarn
	case strings.Contains(up, "INFO"):
		return LevelInfo
	case strings.Contains(up, "DEBUG"), strings.Contains(up, "TRACE"):
		return LevelDebug
	default:
		return ""
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// makeRef builds a stable citation id the UI can resolve back to a row.
func makeRef(provider, stream string, ts time.Time, message string) string {
	h := sha1.Sum([]byte(message))
	return fmt.Sprintf("%s:%s:%d:%x", provider, stream, ts.UnixMilli(), h[:4])
}
