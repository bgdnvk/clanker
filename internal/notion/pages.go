package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// SearchOptions mirrors Notion's `POST /v1/search` body. Filter restricts
// to pages or databases; Sort orders by last_edited_time (the only sort
// the search endpoint accepts).
type SearchOptions struct {
	Query        string // free-text — matches titles only, NOT block content
	FilterObject string // "page" | "database" | "" (both)
	PageSize     int    // 1..100
}

// Search wraps Notion's `POST /v1/search`. Returns RawMessage so callers
// can dispatch on `object` ("page"|"database"). Pagination is opt-out:
// pass PageSize=0 to fetch one page (default 25), or call again with
// the returned NextCursor.
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]json.RawMessage, string, bool, error) {
	body := map[string]any{}
	if q := strings.TrimSpace(opts.Query); q != "" {
		body["query"] = q
	}
	if opts.FilterObject != "" {
		body["filter"] = map[string]string{"property": "object", "value": opts.FilterObject}
	}
	if opts.PageSize > 0 {
		body["page_size"] = opts.PageSize
	}
	body["sort"] = map[string]string{"direction": "descending", "timestamp": "last_edited_time"}

	var resp PaginatedResponse
	if err := c.Do(ctx, "POST", "/search", body, &resp); err != nil {
		return nil, "", false, err
	}
	return resp.Results, resp.NextCursor, resp.HasMore, nil
}

// SearchPages is a typed convenience over Search filtered to pages.
func (c *Client) SearchPages(ctx context.Context, query string, pageSize int) ([]Page, error) {
	results, _, _, err := c.Search(ctx, SearchOptions{
		Query:        query,
		FilterObject: FilterObjectPage,
		PageSize:     pageSize,
	})
	if err != nil {
		return nil, err
	}
	pages := make([]Page, 0, len(results))
	for _, raw := range results {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("decode page: %w", err)
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// GetPage fetches a single page by ID. Notion accepts both
// dash-separated UUIDs and the compact form; we pass through as-is.
func (c *Client) GetPage(ctx context.Context, id string) (*Page, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("page id is required")
	}
	var p Page
	if err := c.Do(ctx, "GET", "/pages/"+url.PathEscape(id), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePage creates a page under a parent (page or database). For
// database parents, properties must satisfy the database schema. For
// page parents, only `title` is meaningful. children is an optional
// block array (use the helpers in blocks.go / markdown.go).
//
// parentType must be "page_id" or "database_id".
func (c *Client) CreatePage(ctx context.Context, parentType, parentID string, properties map[string]any, children []map[string]any) (*Page, error) {
	if parentType != ParentTypePage && parentType != ParentTypeDatabase {
		return nil, fmt.Errorf("parent type must be %q or %q, got %q", ParentTypePage, ParentTypeDatabase, parentType)
	}
	if strings.TrimSpace(parentID) == "" {
		return nil, errors.New("parent id is required")
	}
	body := map[string]any{
		"parent":     map[string]string{parentType: parentID},
		"properties": properties,
	}
	if len(children) > 0 {
		body["children"] = children
	}
	var p Page
	if err := c.Do(ctx, "POST", "/pages", body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePageProperties patches a page's typed property values. Use for
// database rows (free-form pages only have `title`). Pass only the
// properties you want to change — Notion merges the patch.
func (c *Client) UpdatePageProperties(ctx context.Context, id string, properties map[string]any) (*Page, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("page id is required")
	}
	body := map[string]any{"properties": properties}
	var p Page
	if err := c.Do(ctx, "PATCH", "/pages/"+url.PathEscape(id), body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ArchivePage soft-deletes a page (Notion has no hard delete via API).
func (c *Client) ArchivePage(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("page id is required")
	}
	body := map[string]any{"archived": true}
	return c.Do(ctx, "PATCH", "/pages/"+url.PathEscape(id), body, nil)
}

// TitleProperty builds the typed `title` property payload Notion expects
// in CreatePage requests. Database rows whose title column is named
// something other than "title" should patch the result before sending.
func TitleProperty(title string) map[string]any {
	return map[string]any{
		"title": map[string]any{
			"title": []map[string]any{{
				"type": "text",
				"text": map[string]any{"content": title},
			}},
		},
	}
}

// TitleOfPage extracts a best-effort display title from a page's
// properties. Database rows store the title under a property whose name
// is workspace-defined ("Name", "Task", etc); we scan for any property
// with type=title. Free-form pages always use the literal key "title".
func TitleOfPage(p *Page) string {
	if p == nil {
		return ""
	}
	if raw, ok := p.Properties["title"]; ok {
		if s := extractRichTextTitle(raw); s != "" {
			return s
		}
	}
	for _, raw := range p.Properties {
		var probe struct {
			Type  string         `json:"type"`
			Title []RichTextSpan `json:"title"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil && probe.Type == "title" {
			return joinRichText(probe.Title)
		}
	}
	return ""
}

func extractRichTextTitle(raw json.RawMessage) string {
	var probe struct {
		Title []RichTextSpan `json:"title"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return joinRichText(probe.Title)
}

func joinRichText(spans []RichTextSpan) string {
	var sb strings.Builder
	for _, s := range spans {
		sb.WriteString(s.PlainText)
	}
	return sb.String()
}
