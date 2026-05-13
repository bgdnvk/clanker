package sre

import (
	"encoding/json"
	"strings"
	"time"
)

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func utcMinus(d time.Duration) string {
	return time.Now().Add(-d).UTC().Format(time.RFC3339)
}

// jsonParseList attempts to parse v as a JSON array or object,
// falling back to a line slice so callers never get raw JSON strings.
func jsonParseList(v string) any {
	v = strings.TrimSpace(v)
	if v == "" || v == "null" || v == "[]" {
		return []any{}
	}
	var arr []any
	if json.Unmarshal([]byte(v), &arr) == nil {
		return arr
	}
	var obj map[string]any
	if json.Unmarshal([]byte(v), &obj) == nil {
		return obj
	}
	return splitLinesLimited(v, 80)
}

// jsonListLen returns the element count for a JSON array string (best-effort).
func jsonListLen(v string) int {
	var arr []any
	if json.Unmarshal([]byte(strings.TrimSpace(v)), &arr) == nil {
		return len(arr)
	}
	return 0
}
