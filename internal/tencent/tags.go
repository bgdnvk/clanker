package tencent

import "reflect"

// extractTags turns any Tencent SDK tag slice into a flat map[string]string.
//
// Tencent's SDK is annoyingly inconsistent about tag-related types across
// services. CVM and VPC use `Tag {Key, Value}`, CBS uses the same, but CDB
// and several DB services emit `ResourceTag {TagKey, TagValue}`. Lighthouse
// uses yet another shape. Rather than writing one converter per service,
// we use reflection: walk the slice, look for a Key/TagKey/TagKeys field
// and a Value/TagValue field on each element, build the map.
//
// Returns nil when the input is nil, empty, or doesn't look like a tag
// slice. Empty maps are normalised to nil so they marshal as `omitempty`.
//
// Usage:
//
//	type instSummary struct {
//	  ...
//	  Tags map[string]string `json:"tags,omitempty"`
//	}
//	row := instSummary{ Tags: extractTags(in.Tags), ... }
func extractTags(v any) map[string]string {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	out := make(map[string]string, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i)
		if item.Kind() == reflect.Ptr {
			if item.IsNil() {
				continue
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.Struct {
			continue
		}
		key := extractStringField(item, "Key", "TagKey")
		if key == "" {
			continue
		}
		val := extractStringField(item, "Value", "TagValue")
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractStringField returns the first non-nil string-valued field found on
// the struct under any of the candidate names. Pointer-to-string fields are
// dereferenced. Returns "" if none match.
func extractStringField(v reflect.Value, names ...string) string {
	for _, name := range names {
		f := v.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		if f.Kind() == reflect.Ptr {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		if f.Kind() == reflect.String {
			return f.String()
		}
	}
	return ""
}
