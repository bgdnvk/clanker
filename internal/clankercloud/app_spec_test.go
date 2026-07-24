package clankercloud

import (
	"strings"
	"testing"
)

func validTestAppSpec() AppSpec {
	return AppSpec{
		SchemaVersion: 1,
		Title:         "Team workspace",
		Description:   "An interactive read-only snapshot.",
		Theme:         "ocean",
		Blocks: []AppBlock{
			{
				Type:  "metrics",
				Title: "Summary",
				MetricItems: []AppMetric{
					{Label: "Contacts", Value: "12", Detail: "Active"},
				},
			},
			{Type: "text", Body: "Use search, sort, and filters locally."},
			{
				Type:    "table",
				Title:   "Contacts",
				Columns: []string{"Name", "Company"},
				Rows:    [][]string{{"Ada", "Analytical Engines"}},
			},
			{
				Type:      "cards",
				CardItems: []AppCard{{Title: "Follow up", Body: "Call Ada", Meta: "Today"}},
			},
			{Type: "list", ListItems: []string{"Prepare notes"}},
		},
	}
}

func TestDecodeAppSpecJSONAcceptsContractAndHostileDisplayText(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": 1,
		"title": "<script>alert('inert')</script>",
		"description": "https://attacker.example/?q=\"quoted\"",
		"blocks": [
			{"type":"metrics","items":[{"label":"Count","value":"12","detail":"<img src=x>"}]},
			{"type":"text","title":"Notes","body":"</script><script>alert(1)</script>"},
			{"type":"table","columns":["Name","Company"],"rows":[["Ada","A&B"],["Lin","<b>Lab</b>"]]},
			{"type":"cards","items":[{"title":"Card","body":"javascript:alert(1)","meta":"\"quoted\""}]},
			{"type":"list","items":["https://example.test","<svg onload=alert(1)>"]}
		]
	}`)
	spec, err := DecodeAppSpecJSON(raw)
	if err != nil {
		t.Fatalf("DecodeAppSpecJSON: %v", err)
	}
	if spec.Theme != "light" {
		t.Fatalf("default theme = %q, want light", spec.Theme)
	}
	if got := spec.Blocks[1].Body; got != "</script><script>alert(1)</script>" {
		t.Fatalf("hostile-looking text changed: %q", got)
	}
	if err := ValidateAppSpec(spec); err != nil {
		t.Fatalf("ValidateAppSpec: %v", err)
	}
}

func TestDecodeAppSpecJSONRejectsUnknownDuplicateNullAndWrongTypes(t *testing.T) {
	cases := map[string]string{
		"unknown root":  `{"schemaVersion":1,"title":"x","blocks":[{"type":"text","body":"x"}],"html":"<h1>x</h1>"}`,
		"mis-cased":     `{"schemaVersion":1,"Title":"x","blocks":[{"type":"text","body":"x"}]}`,
		"unknown block": `{"schemaVersion":1,"title":"x","blocks":[{"type":"text","body":"x","href":"https://example.test"}]}`,
		"unknown item":  `{"schemaVersion":1,"title":"x","blocks":[{"type":"cards","items":[{"title":"x","url":"https://example.test"}]}]}`,
		"mis-cased block": `{"schemaVersion":1,"title":"x",
			"blocks":[{"Type":"text","body":"x"}]}`,
		"duplicate root": `{"schemaVersion":1,"title":"x","title":"y",
			"blocks":[{"type":"text","body":"x"}]}`,
		"duplicate nested": `{"schemaVersion":1,"title":"x",
			"blocks":[{"type":"text","body":"x","body":"y"}]}`,
		"null optional": `{"schemaVersion":1,"title":"x","description":null,
			"blocks":[{"type":"text","body":"x"}]}`,
		"float version": `{"schemaVersion":1.0,"title":"x",
			"blocks":[{"type":"text","body":"x"}]}`,
		"non-string cell": `{"schemaVersion":1,"title":"x",
			"blocks":[{"type":"table","columns":["x"],"rows":[[1]]}]}`,
		"lone surrogate": `{"schemaVersion":1,"title":"x",
			"blocks":[{"type":"text","body":"\ud800"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAppSpecJSON([]byte(raw)); err == nil {
				t.Fatal("invalid app spec JSON succeeded")
			}
		})
	}
}

func TestRejectDuplicateAppJSONKeysBoundsNestingDepth(t *testing.T) {
	atLimit := strings.Repeat("[", maxAppJSONDepth) + "0" + strings.Repeat("]", maxAppJSONDepth)
	if err := rejectDuplicateAppJSONKeys([]byte(atLimit)); err != nil {
		t.Fatalf("depth %d was rejected: %v", maxAppJSONDepth, err)
	}
	overLimit := "[" + atLimit + "]"
	if err := rejectDuplicateAppJSONKeys([]byte(overLimit)); err == nil ||
		!strings.Contains(err.Error(), "maximum nesting depth") {
		t.Fatalf("depth %d error = %v", maxAppJSONDepth+1, err)
	}
}

func TestDecodeAppSpecJSONRejectsInvalidUTF8AndOversizedRawInput(t *testing.T) {
	invalidUTF8 := []byte(`{"schemaVersion":1,"title":"x","blocks":[{"type":"text","body":"`)
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}]}`)...)
	if _, err := DecodeAppSpecJSON(invalidUTF8); err == nil {
		t.Fatal("invalid UTF-8 app spec succeeded")
	}
	if _, err := DecodeAppSpecJSON(make([]byte, MaxAppSpecBytes+1)); err == nil {
		t.Fatal("oversized raw app spec succeeded")
	}
}

func TestValidateAppSpecRejectsPerBlockAndCumulativeBounds(t *testing.T) {
	tests := map[string]AppSpec{
		"wrong schema": {
			SchemaVersion: 2,
			Title:         "x",
			Blocks:        []AppBlock{{Type: "text", Body: "x"}},
		},
		"blank title": {
			SchemaVersion: 1,
			Title:         "\u2003",
			Blocks:        []AppBlock{{Type: "text", Body: "x"}},
		},
		"bad theme": {
			SchemaVersion: 1,
			Title:         "x",
			Theme:         "custom-css",
			Blocks:        []AppBlock{{Type: "text", Body: "x"}},
		},
		"empty blocks": {
			SchemaVersion: 1,
			Title:         "x",
		},
		"duplicate columns": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{{
				Type: "table", Columns: []string{"Name", "Name"}, Rows: [][]string{},
			}},
		},
		"row width": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{{
				Type: "table", Columns: []string{"Name"}, Rows: [][]string{{"Ada", "extra"}},
			}},
		},
		"missing rows": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{{
				Type: "table", Columns: []string{"Name"},
			}},
		},
		"too many metrics": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{
				{Type: "metrics", MetricItems: repeatedMetrics(12)},
				{Type: "metrics", MetricItems: repeatedMetrics(12)},
				{Type: "metrics", MetricItems: repeatedMetrics(12)},
				{Type: "metrics", MetricItems: repeatedMetrics(12)},
				{Type: "metrics", MetricItems: repeatedMetrics(1)},
			},
		},
		"too many cards": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{
				{Type: "cards", CardItems: repeatedCards(48)},
				{Type: "cards", CardItems: repeatedCards(48)},
				{Type: "cards", CardItems: repeatedCards(1)},
			},
		},
		"too many list items": {
			SchemaVersion: 1,
			Title:         "x",
			Blocks: []AppBlock{
				{Type: "list", ListItems: repeatedStrings("item", 200)},
				{Type: "list", ListItems: repeatedStrings("item", 200)},
				{Type: "list", ListItems: repeatedStrings("item", 1)},
			},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAppSpec(spec); err == nil {
				t.Fatal("invalid app spec succeeded")
			}
		})
	}
}

func TestValidateAppSpecRejectsOversizedCanonicalJSON(t *testing.T) {
	cell := strings.Repeat("x", 500)
	rows := make([][]string, 250)
	for index := range rows {
		rows[index] = repeatedStrings(cell, 6)
	}
	spec := AppSpec{
		SchemaVersion: 1,
		Title:         "Large table",
		Blocks: []AppBlock{
			{Type: "table", Columns: []string{"a", "b", "c", "d", "e", "f"}, Rows: rows},
			{Type: "table", Columns: []string{"a", "b", "c", "d", "e", "f"}, Rows: rows},
		},
	}
	if err := ValidateAppSpec(spec); err == nil || !strings.Contains(err.Error(), "canonical app spec exceeds") {
		t.Fatalf("oversized canonical app spec error = %v", err)
	}
}

func TestValidateAppSpecRejectsUnsafeUnicodeAndCredentialSignatures(t *testing.T) {
	values := []string{
		"nul\u0000byte",
		"delete\u007fcontrol",
		"replacement\ufffdcharacter",
		"line\u2028separator",
		"paragraph\u2029separator",
		"bidi\u202etext",
		"zero\u200bwidth",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"AKIA1234567890ABCDEF",
		"AIza12345678901234567890123456789012345",
		"github_pat_123456789012345678901234567890",
		"ghp_123456789012345678901234567890",
		"xoxb-12345678901234567890",
		"sk-123456789012345678901234",
		"Authorization: Bearer abcdefghijklmnopqrst",
		"CLIENT-SECRET=abcdefghijklmnopqrstuvwxyz",
	}
	for _, value := range values {
		spec := AppSpec{
			SchemaVersion: 1,
			Title:         "Security",
			Blocks:        []AppBlock{{Type: "text", Body: value}},
		}
		if err := ValidateAppSpec(spec); err == nil {
			t.Fatalf("unsafe or credential-like value %q succeeded", value)
		}
	}
}

func TestCanonicalAppSpecJSONUsesStableSafeEncoding(t *testing.T) {
	spec := AppSpec{
		SchemaVersion: 1,
		Title:         "<CRM>&",
		Description:   "",
		Theme:         "light",
		Blocks:        []AppBlock{{Type: "text", Body: "A&B"}},
	}
	got, err := canonicalAppSpecJSON(spec)
	if err != nil {
		t.Fatalf("canonicalAppSpecJSON: %v", err)
	}
	want := `{"blocks":[{"body":"A&B","type":"text"}],"schemaVersion":1,"theme":"light","title":"<CRM>&"}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestDecodeAppSpecJSONCanonicalizesWhitespaceOnlyOptionalStringsToAbsent(t *testing.T) {
	spec, err := DecodeAppSpecJSON([]byte(`{
		"schemaVersion": 1,
		"title": "Whitespace",
		"description": "   ",
		"blocks": [
			{"type":"metrics","title":"  ","items":[{"label":"A","value":"1","detail":" \t "}]},
			{"type":"cards","items":[{"title":"Card","body":" \n ","meta":"  "}]}
		]
	}`))
	if err != nil {
		t.Fatalf("DecodeAppSpecJSON: %v", err)
	}
	if spec.Description != "" || spec.Blocks[0].Title != "" ||
		spec.Blocks[0].MetricItems[0].Detail != "" ||
		spec.Blocks[1].CardItems[0].Body != "" ||
		spec.Blocks[1].CardItems[0].Meta != "" {
		t.Fatalf("whitespace-only optional strings were retained: %#v", spec)
	}
	canonical, err := canonicalAppSpecJSON(spec)
	if err != nil {
		t.Fatalf("canonicalAppSpecJSON: %v", err)
	}
	want := `{"blocks":[{"items":[{"label":"A","value":"1"}],"type":"metrics"},{"items":[{"title":"Card"}],"type":"cards"}],"schemaVersion":1,"theme":"light","title":"Whitespace"}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON = %s, want %s", canonical, want)
	}
}

func repeatedMetrics(count int) []AppMetric {
	items := make([]AppMetric, count)
	for index := range items {
		items[index] = AppMetric{Label: "label", Value: "value"}
	}
	return items
}

func repeatedCards(count int) []AppCard {
	items := make([]AppCard, count)
	for index := range items {
		items[index] = AppCard{Title: "card"}
	}
	return items
}

func repeatedStrings(value string, count int) []string {
	items := make([]string, count)
	for index := range items {
		items[index] = value
	}
	return items
}
