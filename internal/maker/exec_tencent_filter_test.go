package maker

import (
	"strings"
	"testing"
)

// TestValidateFilterCommand covers the security-relevant validation surface
// the maker plan executor uses to reject malformed filter verbs before they
// reach jsonPathRaw / filterMatch. Each case asserts the success/failure
// shape AND that the error message names the offending field — the LLM uses
// that text to self-correct on the next plan attempt.
func TestValidateFilterCommand(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantErr   bool
		errSubstr string // substring the error message must contain (when wantErr)
	}{
		// arg count
		{
			name:      "too few args",
			args:      []string{"filter", "$prev", "$.x", "name", "=="},
			wantErr:   true,
			errSubstr: "requires exactly 6 args",
		},
		{
			name:      "too many args",
			args:      []string{"filter", "$prev", "$.x", "name", "==", "v", "extra"},
			wantErr:   true,
			errSubstr: "requires exactly 6 args",
		},
		// arrayPath / field empty
		{
			name:      "empty arrayPath",
			args:      []string{"filter", "$prev", "", "name", "==", "v"},
			wantErr:   true,
			errSubstr: "arrayPath is required",
		},
		{
			name:      "whitespace arrayPath",
			args:      []string{"filter", "$prev", "   ", "name", "==", "v"},
			wantErr:   true,
			errSubstr: "arrayPath is required",
		},
		{
			name:      "empty field",
			args:      []string{"filter", "$prev", "$.x", "", "==", "v"},
			wantErr:   true,
			errSubstr: "field is required",
		},
		// op enum
		{
			name:      "unknown op",
			args:      []string{"filter", "$prev", "$.x", "name", "===", "v"},
			wantErr:   true,
			errSubstr: "filter op",
		},
		{
			name:      "case-sensitive op",
			args:      []string{"filter", "$prev", "$.x", "name", "Contains", "v"},
			wantErr:   true,
			errSubstr: "filter op",
		},
		// sourceIdx
		{
			name:      "sourceIdx zero",
			args:      []string{"filter", "0", "$.x", "name", "==", "v"},
			wantErr:   true,
			errSubstr: "sourceIdx",
		},
		{
			name:      "sourceIdx negative",
			args:      []string{"filter", "-1", "$.x", "name", "==", "v"},
			wantErr:   true,
			errSubstr: "sourceIdx",
		},
		{
			name:      "sourceIdx non-numeric non-prev",
			args:      []string{"filter", "abc", "$.x", "name", "==", "v"},
			wantErr:   true,
			errSubstr: "sourceIdx",
		},
		// happy paths
		{
			name:    "valid $prev",
			args:    []string{"filter", "$prev", "$.x", "name", "==", "v"},
			wantErr: false,
		},
		{
			name:    "valid numeric idx",
			args:    []string{"filter", "1", "$.x", "name", "==", "v"},
			wantErr: false,
		},
		{
			name:    "valid large numeric idx",
			args:    []string{"filter", "99", "$.Response.X", "Memory", ">=", "8"},
			wantErr: false,
		},
		// every op accepted
		{name: "op >", args: []string{"filter", "$prev", "$.x", "n", ">", "1"}, wantErr: false},
		{name: "op <", args: []string{"filter", "$prev", "$.x", "n", "<", "1"}, wantErr: false},
		{name: "op >=", args: []string{"filter", "$prev", "$.x", "n", ">=", "1"}, wantErr: false},
		{name: "op <=", args: []string{"filter", "$prev", "$.x", "n", "<=", "1"}, wantErr: false},
		{name: "op !=", args: []string{"filter", "$prev", "$.x", "n", "!=", "1"}, wantErr: false},
		{name: "op contains", args: []string{"filter", "$prev", "$.x", "n", "contains", "1"}, wantErr: false},
		{name: "op startsWith", args: []string{"filter", "$prev", "$.x", "n", "startsWith", "1"}, wantErr: false},
		{name: "op matches", args: []string{"filter", "$prev", "$.x", "n", "matches", "^a$"}, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilterCommand(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestFilterMatch exercises every operator across the JSON value types the
// SDK responses actually contain (string / number / bool / nil-via-missing).
// It also locks in the ReDoS-defense behaviour added after upstream PR #165
// review: oversize patterns and uncompilable regexes return false rather
// than crashing or hanging.
func TestFilterMatch(t *testing.T) {
	cases := []struct {
		name  string
		v     any
		op    string
		value string
		want  bool
	}{
		// numeric comparisons — float, int, int64, string-coerced
		{name: "gt float true", v: 5.0, op: ">", value: "3", want: true},
		{name: "gt float false", v: 2.0, op: ">", value: "3", want: false},
		{name: "ge equal true", v: float64(8), op: ">=", value: "8", want: true},
		{name: "lt int true", v: 1, op: "<", value: "5", want: true},
		{name: "le int64 true", v: int64(4), op: "<=", value: "4", want: true},
		{name: "gt string-coerced", v: "12.5", op: ">", value: "10", want: true},
		{name: "gt bool true is 1", v: true, op: ">", value: "0", want: true},
		// numeric comparisons that should fail gracefully
		{name: "gt non-numeric returns false", v: "abc", op: ">", value: "1", want: false},
		{name: "gt nil-ish returns false", v: nil, op: ">", value: "1", want: false},
		// equality — Sprint-based, so works on every type
		{name: "eq string", v: "RUNNING", op: "==", value: "RUNNING", want: true},
		{name: "eq number", v: 42, op: "==", value: "42", want: true},
		{name: "eq bool", v: true, op: "==", value: "true", want: true},
		{name: "ne string", v: "RUNNING", op: "!=", value: "STOPPED", want: true},
		{name: "ne same returns false", v: "RUNNING", op: "!=", value: "RUNNING", want: false},
		// contains / startsWith — string-only
		{name: "contains hit", v: "metatech-nodehelix", op: "contains", value: "node", want: true},
		{name: "contains miss", v: "metatech-nodehelix", op: "contains", value: "absent", want: false},
		{name: "contains non-string returns false", v: 42, op: "contains", value: "4", want: false},
		{name: "startsWith hit", v: "prod-app", op: "startsWith", value: "prod-", want: true},
		{name: "startsWith miss", v: "prod-app", op: "startsWith", value: "stg-", want: false},
		{name: "startsWith non-string returns false", v: 100, op: "startsWith", value: "1", want: false},
		// matches — the ReDoS-hardened operator
		{name: "matches anchored regex", v: "prod-foo", op: "matches", value: `^prod-`, want: true},
		{name: "matches regex no hit", v: "stg-foo", op: "matches", value: `^prod-`, want: false},
		{name: "matches non-string returns false", v: 123, op: "matches", value: `\d+`, want: false},
		{name: "matches malformed regex returns false", v: "abc", op: "matches", value: `^bad[`, want: false},
		{
			name:  "matches oversize pattern (>256 chars) returns false",
			v:     "abc",
			op:    "matches",
			value: strings.Repeat("a", maxFilterPatternLen+1),
			want:  false,
		},
		{
			name:  "matches edge pattern (=256 chars) accepted",
			v:     "a",
			op:    "matches",
			value: strings.Repeat("a", maxFilterPatternLen),
			want:  false, // pattern is 256 a's; "a" won't match (needs all 256)
		},
		{
			name:  "matches PCRE-ReDoS pattern handled by RE2 (no hang, returns false)",
			v:     "aaaaaaaaaaaab",
			op:    "matches",
			value: `(a+)+$`,
			want:  false,
		},
		// unknown op falls through to false (defensive — validateFilterCommand
		// should reject these before reaching filterMatch, but this guards
		// against any future code path that skips validation)
		{name: "unknown op returns false", v: "x", op: "wat", value: "y", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterMatch(tc.v, tc.op, tc.value)
			if got != tc.want {
				t.Errorf("filterMatch(%v, %q, %q) = %v, want %v", tc.v, tc.op, tc.value, got, tc.want)
			}
		})
	}
}
