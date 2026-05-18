package maker

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// validFilterOps lists the operators the filter verb accepts. Kept small and
// explicit so the LLM has a finite set to choose from.
var validFilterOps = map[string]bool{
	">":          true,
	"<":          true,
	">=":         true,
	"<=":         true,
	"==":         true,
	"!=":         true,
	"contains":   true,
	"startsWith": true,
	"matches":    true,
}

// validateFilterCommand checks the shape of a filter-verb command. Schema:
//
//	["filter", "<sourceIdx>", "<arrayPath>", "<field>", "<op>", "<value>"]
//
// sourceIdx is either "$prev" or a 1-based numeric index.
// arrayPath is a JSONPath that, when applied to the source body, resolves to
// an array of objects. field is a JSONPath inside each element. op is one of
// validFilterOps. value is parsed as a number when op is numeric.
func validateFilterCommand(args []string) error {
	if len(args) != 6 {
		return fmt.Errorf("filter verb requires exactly 6 args [filter, <sourceIdx>, <arrayPath>, <field>, <op>, <value>], got %d", len(args))
	}
	if strings.TrimSpace(args[2]) == "" {
		return fmt.Errorf("filter arrayPath is required (e.g. $.Response.InstanceSet)")
	}
	if strings.TrimSpace(args[3]) == "" {
		return fmt.Errorf("filter field is required (e.g. Memory or InstanceState)")
	}
	if !validFilterOps[args[4]] {
		ops := make([]string, 0, len(validFilterOps))
		for k := range validFilterOps {
			ops = append(ops, k)
		}
		return fmt.Errorf("filter op %q invalid; must be one of: %s", args[4], strings.Join(ops, " "))
	}
	src := strings.TrimSpace(args[1])
	if src == "$prev" {
		return nil
	}
	if n, err := strconv.Atoi(src); err != nil || n < 1 {
		return fmt.Errorf("filter sourceIdx %q invalid; must be a 1-based index or $prev", args[1])
	}
	return nil
}

// executeFilter runs a filter command against priorOutputs and returns the
// filtered subset as a JSON document. The shape is:
//
//	{ "matched": <N>, "total_in": <M>, "field": "...", "op": "...", "value": "...", "items": [...] }
//
// "items" is the subset of elements from the resolved array that satisfied
// the predicate. The full input is NOT echoed back to keep output small.
func executeFilter(args []string, priorOutputs []string) (string, error) {
	if err := validateFilterCommand(args); err != nil {
		return "", err
	}
	sourceIdx := strings.TrimSpace(args[1])
	arrayPath := strings.TrimSpace(args[2])
	field := strings.TrimSpace(args[3])
	op := args[4]
	value := args[5]

	// Resolve source body.
	var sourceBody string
	if sourceIdx == "$prev" {
		if len(priorOutputs) == 0 {
			return "", fmt.Errorf("filter sourceIdx=$prev but there are no prior commands")
		}
		sourceBody = priorOutputs[len(priorOutputs)-1]
	} else {
		n, _ := strconv.Atoi(sourceIdx)
		if n < 1 || n > len(priorOutputs) {
			return "", fmt.Errorf("filter sourceIdx=%d out of range (1..%d prior commands)", n, len(priorOutputs))
		}
		sourceBody = priorOutputs[n-1]
	}
	if strings.TrimSpace(sourceBody) == "" {
		return "", fmt.Errorf("filter source command produced no output to filter")
	}

	// Parse source.
	var raw any
	if err := json.Unmarshal([]byte(sourceBody), &raw); err != nil {
		return "", fmt.Errorf("filter source body is not valid JSON: %w", err)
	}

	// Resolve arrayPath. Trim a trailing [*] if present (LLM often appends it).
	cleanPath := strings.TrimSuffix(strings.TrimSpace(arrayPath), "[*]")
	resolved, ok := jsonPathRaw(raw, cleanPath)
	if !ok {
		return "", fmt.Errorf("filter arrayPath %q did not resolve in the source body", arrayPath)
	}
	items, ok := resolved.([]any)
	if !ok {
		return "", fmt.Errorf("filter arrayPath %q resolved to a non-array (got %T) — point at an array of items like $.Response.InstanceSet", arrayPath, resolved)
	}

	// Filter.
	matched := make([]any, 0, len(items))
	for _, item := range items {
		v, ok := jsonPathRaw(item, field)
		if !ok {
			continue
		}
		if filterMatch(v, op, value) {
			matched = append(matched, item)
		}
	}

	out := struct {
		Matched int    `json:"matched"`
		TotalIn int    `json:"total_in"`
		Field   string `json:"field"`
		Op      string `json:"op"`
		Value   string `json:"value"`
		Items   []any  `json:"items"`
	}{
		Matched: len(matched),
		TotalIn: len(items),
		Field:   field,
		Op:      op,
		Value:   value,
		Items:   matched,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("filter result marshal: %w", err)
	}
	return string(b), nil
}

// filterMatch evaluates one predicate. Numeric operators auto-convert string
// values that parse cleanly; non-parseable comparisons return false rather
// than erroring so a single bad item doesn't poison the run.
func filterMatch(v any, op, value string) bool {
	switch op {
	case ">", "<", ">=", "<=":
		a, ok1 := toFloat(v)
		b, ok2 := toFloat(value)
		if !ok1 || !ok2 {
			return false
		}
		switch op {
		case ">":
			return a > b
		case "<":
			return a < b
		case ">=":
			return a >= b
		case "<=":
			return a <= b
		}
	case "==":
		return fmt.Sprint(v) == value
	case "!=":
		return fmt.Sprint(v) != value
	case "contains":
		s, ok := v.(string)
		return ok && strings.Contains(s, value)
	case "startsWith":
		s, ok := v.(string)
		return ok && strings.HasPrefix(s, value)
	case "matches":
		s, ok := v.(string)
		if !ok {
			return false
		}
		re, err := regexp.Compile(value)
		return err == nil && re.MatchString(s)
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f, true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
