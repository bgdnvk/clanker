package clankercloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	AppSpecSchemaVersion = 1
	MaxAppSpecBytes      = 256 << 10
	maxAppJSONDepth      = 64
)

var (
	appSpecAWSAccessKeyPattern = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	appSpecGoogleAPIKeyPattern = regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)
	appSpecGitHubTokenPattern  = regexp.MustCompile(`\b(github_pat_[0-9A-Za-z_]{30,}|gh[pousr]_[0-9A-Za-z]{30,})\b`)
	appSpecSlackTokenPattern   = regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{20,}\b`)
	appSpecOpenAITokenPattern  = regexp.MustCompile(`\bsk-[0-9A-Za-z_-]{24,}\b`)
	appSpecBearerPattern       = regexp.MustCompile(`(?i)\bAuthorization\s*:\s*Bearer\s+[0-9A-Za-z._~+/-]{20,}`)
	appSpecAssignmentPattern   = regexp.MustCompile(`(?i)\b(API[_-]?KEY|ACCESS[_-]?TOKEN|AUTH[_-]?TOKEN|CLIENT[_-]?SECRET|PASSWORD)\s*[:=]\s*["']?(sk-|AKIA|AIza|gh[pousr]_|github_pat_|xox|[0-9A-Za-z+/=_-]{24,})`)
	appSpecPrivateKeyPattern   = regexp.MustCompile(`-----BEGIN (RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`)
)

// AppSpec is the fixed-renderer deployment option. Every value remains data;
// use the HTML deployment option when the app needs executable browser code.
type AppSpec struct {
	SchemaVersion int        `json:"schemaVersion"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	Theme         string     `json:"theme,omitempty"`
	Blocks        []AppBlock `json:"blocks"`
}

type AppMetric struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
}

type AppCard struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Meta  string `json:"meta,omitempty"`
}

// AppBlock is a tagged union. Only the fields belonging to Type are encoded.
type AppBlock struct {
	Type        string
	Title       string
	MetricItems []AppMetric
	Body        string
	Columns     []string
	Rows        [][]string
	CardItems   []AppCard
	ListItems   []string
}

func (block AppBlock) MarshalJSON() ([]byte, error) {
	switch block.Type {
	case "metrics":
		return marshalUnescapedAppJSON(struct {
			Type  string      `json:"type"`
			Title string      `json:"title,omitempty"`
			Items []AppMetric `json:"items"`
		}{Type: block.Type, Title: block.Title, Items: block.MetricItems})
	case "text":
		return marshalUnescapedAppJSON(struct {
			Type  string `json:"type"`
			Title string `json:"title,omitempty"`
			Body  string `json:"body"`
		}{Type: block.Type, Title: block.Title, Body: block.Body})
	case "table":
		return marshalUnescapedAppJSON(struct {
			Type    string     `json:"type"`
			Title   string     `json:"title,omitempty"`
			Columns []string   `json:"columns"`
			Rows    [][]string `json:"rows"`
		}{Type: block.Type, Title: block.Title, Columns: block.Columns, Rows: block.Rows})
	case "cards":
		return marshalUnescapedAppJSON(struct {
			Type  string    `json:"type"`
			Title string    `json:"title,omitempty"`
			Items []AppCard `json:"items"`
		}{Type: block.Type, Title: block.Title, Items: block.CardItems})
	case "list":
		return marshalUnescapedAppJSON(struct {
			Type  string   `json:"type"`
			Title string   `json:"title,omitempty"`
			Items []string `json:"items"`
		}{Type: block.Type, Title: block.Title, Items: block.ListItems})
	default:
		return nil, fmt.Errorf("unsupported app block type %q", block.Type)
	}
}

func marshalUnescapedAppJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (block *AppBlock) UnmarshalJSON(data []byte) error {
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return err
	}

	switch discriminator.Type {
	case "metrics":
		var value struct {
			Type  string      `json:"type"`
			Title string      `json:"title,omitempty"`
			Items []AppMetric `json:"items"`
		}
		if err := decodeStrictAppJSON(data, &value); err != nil {
			return err
		}
		*block = AppBlock{Type: value.Type, Title: value.Title, MetricItems: value.Items}
	case "text":
		var value struct {
			Type  string `json:"type"`
			Title string `json:"title,omitempty"`
			Body  string `json:"body"`
		}
		if err := decodeStrictAppJSON(data, &value); err != nil {
			return err
		}
		*block = AppBlock{Type: value.Type, Title: value.Title, Body: value.Body}
	case "table":
		var value struct {
			Type    string     `json:"type"`
			Title   string     `json:"title,omitempty"`
			Columns []string   `json:"columns"`
			Rows    [][]string `json:"rows"`
		}
		if err := decodeStrictAppJSON(data, &value); err != nil {
			return err
		}
		*block = AppBlock{Type: value.Type, Title: value.Title, Columns: value.Columns, Rows: value.Rows}
	case "cards":
		var value struct {
			Type  string    `json:"type"`
			Title string    `json:"title,omitempty"`
			Items []AppCard `json:"items"`
		}
		if err := decodeStrictAppJSON(data, &value); err != nil {
			return err
		}
		*block = AppBlock{Type: value.Type, Title: value.Title, CardItems: value.Items}
	case "list":
		var value struct {
			Type  string   `json:"type"`
			Title string   `json:"title,omitempty"`
			Items []string `json:"items"`
		}
		if err := decodeStrictAppJSON(data, &value); err != nil {
			return err
		}
		*block = AppBlock{Type: value.Type, Title: value.Title, ListItems: value.Items}
	default:
		return fmt.Errorf("unsupported app block type %q", discriminator.Type)
	}
	return nil
}

// DecodeAppSpecJSON rejects duplicate and unknown keys before applying the
// same structural and content limits as CreateDeployment.
func DecodeAppSpecJSON(data []byte) (AppSpec, error) {
	if len(data) == 0 {
		return AppSpec{}, fmt.Errorf("app spec JSON is required")
	}
	if len(data) > MaxAppSpecBytes {
		return AppSpec{}, fmt.Errorf("app spec JSON exceeds %d bytes", MaxAppSpecBytes)
	}
	if !utf8.Valid(data) {
		return AppSpec{}, fmt.Errorf("app spec JSON must be valid UTF-8")
	}
	if err := rejectDuplicateAppJSONKeys(data); err != nil {
		return AppSpec{}, err
	}
	if err := validateRawAppSpecShape(data); err != nil {
		return AppSpec{}, err
	}
	var spec AppSpec
	if err := decodeStrictAppJSON(data, &spec); err != nil {
		return AppSpec{}, err
	}
	spec = normalizeAppSpec(spec)
	if err := ValidateAppSpec(spec); err != nil {
		return AppSpec{}, err
	}
	return spec, nil
}

func decodeStrictAppJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := requireAppJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func requireAppJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("app spec JSON must contain exactly one value")
		}
		return err
	}
	return nil
}

func rejectDuplicateAppJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, isDelim := token.(json.Delim)
		if !isDelim {
			return nil
		}
		if depth >= maxAppJSONDepth {
			return fmt.Errorf("app spec JSON exceeds the maximum nesting depth of %d", maxAppJSONDepth)
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("app spec object key must be a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate app spec key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return fmt.Errorf("invalid app spec object")
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return fmt.Errorf("invalid app spec array")
			}
		default:
			return fmt.Errorf("unexpected app spec delimiter %q", delim)
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	return requireAppJSONEOF(decoder)
}

func validateRawAppSpecShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var rejectNulls func(any) error
	rejectNulls = func(current any) error {
		switch typed := current.(type) {
		case nil:
			return fmt.Errorf("app spec fields must not be null")
		case []any:
			for _, item := range typed {
				if err := rejectNulls(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for _, item := range typed {
				if err := rejectNulls(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := rejectNulls(value); err != nil {
		return err
	}

	root, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("app spec must be a JSON object")
	}
	if err := requireExactAppKeys("app spec", root, "schemaVersion", "title", "description", "theme", "blocks"); err != nil {
		return err
	}
	rawBlocks, ok := root["blocks"].([]any)
	if !ok {
		return fmt.Errorf("app spec blocks must be an array")
	}
	for blockIndex, rawBlock := range rawBlocks {
		field := fmt.Sprintf("app spec blocks[%d]", blockIndex)
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", field)
		}
		blockType, ok := block["type"].(string)
		if !ok {
			return fmt.Errorf("%s type must be a string", field)
		}
		switch blockType {
		case "metrics":
			if err := requireExactAppKeys(field, block, "type", "title", "items"); err != nil {
				return err
			}
			if err := validateRawAppItemObjects(field, block["items"], "label", "value", "detail"); err != nil {
				return err
			}
		case "text":
			if err := requireExactAppKeys(field, block, "type", "title", "body"); err != nil {
				return err
			}
		case "table":
			if err := requireExactAppKeys(field, block, "type", "title", "columns", "rows"); err != nil {
				return err
			}
		case "cards":
			if err := requireExactAppKeys(field, block, "type", "title", "items"); err != nil {
				return err
			}
			if err := validateRawAppItemObjects(field, block["items"], "title", "body", "meta"); err != nil {
				return err
			}
		case "list":
			if err := requireExactAppKeys(field, block, "type", "title", "items"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported app block type %q", blockType)
		}
	}
	return nil
}

func requireExactAppKeys(field string, value map[string]any, allowed ...string) error {
	allowlist := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowlist[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowlist[key]; !ok {
			return fmt.Errorf("%s contains unknown key %q", field, key)
		}
	}
	return nil
}

func validateRawAppItemObjects(field string, value any, allowed ...string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s items must be an array", field)
	}
	for itemIndex, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return fmt.Errorf("%s items[%d] must be an object", field, itemIndex)
		}
		if err := requireExactAppKeys(fmt.Sprintf("%s items[%d]", field, itemIndex), item, allowed...); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAppSpec(spec AppSpec) AppSpec {
	if spec.Theme == "" {
		spec.Theme = "light"
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = ""
	}
	for blockIndex := range spec.Blocks {
		block := &spec.Blocks[blockIndex]
		if strings.TrimSpace(block.Title) == "" {
			block.Title = ""
		}
		for itemIndex := range block.MetricItems {
			if strings.TrimSpace(block.MetricItems[itemIndex].Detail) == "" {
				block.MetricItems[itemIndex].Detail = ""
			}
		}
		for itemIndex := range block.CardItems {
			if strings.TrimSpace(block.CardItems[itemIndex].Body) == "" {
				block.CardItems[itemIndex].Body = ""
			}
			if strings.TrimSpace(block.CardItems[itemIndex].Meta) == "" {
				block.CardItems[itemIndex].Meta = ""
			}
		}
	}
	return spec
}

// ValidateAppSpec validates the declarative v1 limits. The server remains
// authoritative, but local validation avoids sending malformed or secret-like
// public data.
func ValidateAppSpec(spec AppSpec) error {
	if spec.SchemaVersion != AppSpecSchemaVersion {
		return fmt.Errorf("app spec schemaVersion must be %d", AppSpecSchemaVersion)
	}
	if err := validateRequiredAppString("app spec title", spec.Title, 120); err != nil {
		return err
	}
	if err := validateOptionalAppString("app spec description", spec.Description, 1000); err != nil {
		return err
	}
	switch spec.Theme {
	case "", "light", "dark", "slate", "ocean":
	default:
		return fmt.Errorf("app spec theme must be one of light, dark, slate, or ocean")
	}
	if len(spec.Blocks) < 1 || len(spec.Blocks) > 24 {
		return fmt.Errorf("app spec blocks must contain 1-24 entries")
	}

	var totalMetrics int
	var totalTableRows int
	var totalTableCells int
	var totalCards int
	var totalListItems int
	for blockIndex, block := range spec.Blocks {
		field := fmt.Sprintf("app spec blocks[%d]", blockIndex)
		if err := validateOptionalAppString(field+" title", block.Title, 120); err != nil {
			return err
		}
		switch block.Type {
		case "metrics":
			if len(block.MetricItems) < 1 || len(block.MetricItems) > 12 {
				return fmt.Errorf("%s metrics items must contain 1-12 entries", field)
			}
			totalMetrics += len(block.MetricItems)
			for itemIndex, item := range block.MetricItems {
				itemField := fmt.Sprintf("%s items[%d]", field, itemIndex)
				if err := validateRequiredAppString(itemField+" label", item.Label, 80); err != nil {
					return err
				}
				if err := validateRequiredAppString(itemField+" value", item.Value, 120); err != nil {
					return err
				}
				if err := validateOptionalAppString(itemField+" detail", item.Detail, 240); err != nil {
					return err
				}
			}
		case "text":
			if err := validateRequiredAppString(field+" body", block.Body, 8000); err != nil {
				return err
			}
		case "table":
			if len(block.Columns) < 1 || len(block.Columns) > 12 {
				return fmt.Errorf("%s columns must contain 1-12 entries", field)
			}
			if block.Rows == nil || len(block.Rows) > 250 {
				return fmt.Errorf("%s rows must contain 0-250 entries", field)
			}
			columns := make(map[string]struct{}, len(block.Columns))
			for columnIndex, column := range block.Columns {
				if err := validateRequiredAppString(fmt.Sprintf("%s columns[%d]", field, columnIndex), column, 80); err != nil {
					return err
				}
				if _, duplicate := columns[column]; duplicate {
					return fmt.Errorf("%s columns must be unique", field)
				}
				columns[column] = struct{}{}
			}
			totalTableRows += len(block.Rows)
			for rowIndex, row := range block.Rows {
				if len(row) != len(block.Columns) {
					return fmt.Errorf("%s rows[%d] must contain exactly %d cells", field, rowIndex, len(block.Columns))
				}
				totalTableCells += len(row)
				for cellIndex, cell := range row {
					if err := validateOptionalAppString(fmt.Sprintf("%s rows[%d][%d]", field, rowIndex, cellIndex), cell, 500); err != nil {
						return err
					}
				}
			}
		case "cards":
			if len(block.CardItems) < 1 || len(block.CardItems) > 48 {
				return fmt.Errorf("%s card items must contain 1-48 entries", field)
			}
			totalCards += len(block.CardItems)
			for itemIndex, item := range block.CardItems {
				itemField := fmt.Sprintf("%s items[%d]", field, itemIndex)
				if err := validateRequiredAppString(itemField+" title", item.Title, 120); err != nil {
					return err
				}
				if err := validateOptionalAppString(itemField+" body", item.Body, 2000); err != nil {
					return err
				}
				if err := validateOptionalAppString(itemField+" meta", item.Meta, 240); err != nil {
					return err
				}
			}
		case "list":
			if len(block.ListItems) < 1 || len(block.ListItems) > 200 {
				return fmt.Errorf("%s list items must contain 1-200 entries", field)
			}
			totalListItems += len(block.ListItems)
			for itemIndex, item := range block.ListItems {
				if err := validateRequiredAppString(fmt.Sprintf("%s items[%d]", field, itemIndex), item, 1000); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%s type must be one of metrics, text, table, cards, or list", field)
		}
	}
	if totalMetrics > 48 {
		return fmt.Errorf("app spec contains %d metric items; maximum is 48", totalMetrics)
	}
	if totalTableRows > 500 {
		return fmt.Errorf("app spec contains %d table rows; maximum is 500", totalTableRows)
	}
	if totalTableCells > 3000 {
		return fmt.Errorf("app spec contains %d table cells; maximum is 3000", totalTableCells)
	}
	if totalCards > 96 {
		return fmt.Errorf("app spec contains %d card items; maximum is 96", totalCards)
	}
	if totalListItems > 400 {
		return fmt.Errorf("app spec contains %d list items; maximum is 400", totalListItems)
	}
	canonical, err := canonicalAppSpecJSON(normalizeAppSpec(spec))
	if err != nil {
		return fmt.Errorf("encode canonical app spec: %w", err)
	}
	if len(canonical) > MaxAppSpecBytes {
		return fmt.Errorf("canonical app spec exceeds %d bytes", MaxAppSpecBytes)
	}
	return nil
}

func validateRequiredAppString(field string, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be blank", field)
	}
	return validateAppString(field, value, maxRunes)
}

func validateOptionalAppString(field string, value string, maxRunes int) error {
	if value == "" {
		return nil
	}
	return validateAppString(field, value, maxRunes)
}

func validateAppString(field string, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	for _, character := range value {
		if isUnsafeAppRune(character) {
			return fmt.Errorf("%s contains an unsafe Unicode control character", field)
		}
	}
	if containsAppCredential(value) {
		return fmt.Errorf("%s appears to contain credential material", field)
	}
	return nil
}

func isUnsafeAppRune(character rune) bool {
	if character == utf8.RuneError {
		return true
	}
	if character >= 0 && character <= 0x1f {
		return character != '\t' && character != '\n' && character != '\r'
	}
	if character >= 0x7f && character <= 0x9f {
		return true
	}
	switch character {
	case '\u061c', '\u2060', '\ufeff':
		return true
	}
	return character >= '\u200b' && character <= '\u200f' ||
		character >= '\u2028' && character <= '\u2029' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069' ||
		unicode.Is(unicode.Cs, character)
}

func containsAppCredential(value string) bool {
	return appSpecPrivateKeyPattern.MatchString(value) ||
		appSpecAWSAccessKeyPattern.MatchString(value) ||
		appSpecGoogleAPIKeyPattern.MatchString(value) ||
		appSpecGitHubTokenPattern.MatchString(value) ||
		appSpecSlackTokenPattern.MatchString(value) ||
		appSpecOpenAITokenPattern.MatchString(value) ||
		appSpecBearerPattern.MatchString(value) ||
		appSpecAssignmentPattern.MatchString(value)
}

func canonicalAppSpecJSON(spec AppSpec) ([]byte, error) {
	blocks := make([]any, 0, len(spec.Blocks))
	for _, block := range spec.Blocks {
		value := map[string]any{"type": block.Type}
		if block.Title != "" {
			value["title"] = block.Title
		}
		switch block.Type {
		case "metrics":
			items := make([]any, 0, len(block.MetricItems))
			for _, item := range block.MetricItems {
				itemValue := map[string]any{"label": item.Label, "value": item.Value}
				if item.Detail != "" {
					itemValue["detail"] = item.Detail
				}
				items = append(items, itemValue)
			}
			value["items"] = items
		case "text":
			value["body"] = block.Body
		case "table":
			value["columns"] = block.Columns
			value["rows"] = block.Rows
		case "cards":
			items := make([]any, 0, len(block.CardItems))
			for _, item := range block.CardItems {
				itemValue := map[string]any{"title": item.Title}
				if item.Body != "" {
					itemValue["body"] = item.Body
				}
				if item.Meta != "" {
					itemValue["meta"] = item.Meta
				}
				items = append(items, itemValue)
			}
			value["items"] = items
		case "list":
			value["items"] = block.ListItems
		default:
			return nil, fmt.Errorf("unsupported app block type %q", block.Type)
		}
		blocks = append(blocks, value)
	}
	value := map[string]any{
		"blocks":        blocks,
		"schemaVersion": spec.SchemaVersion,
		"theme":         spec.Theme,
		"title":         spec.Title,
	}
	if spec.Description != "" {
		value["description"] = spec.Description
	}
	return marshalUnescapedAppJSON(value)
}
