package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ListDatabases enumerates databases via the search endpoint with the
// `object=database` filter. Notion's "list databases" endpoint is
// deprecated; search is the supported path.
func (c *Client) ListDatabases(ctx context.Context, query string, pageSize int) ([]Database, error) {
	results, _, _, err := c.Search(ctx, SearchOptions{
		Query:        query,
		FilterObject: FilterObjectDatabase,
		PageSize:     pageSize,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Database, 0, len(results))
	for _, raw := range results {
		var db Database
		if err := json.Unmarshal(raw, &db); err != nil {
			return nil, fmt.Errorf("decode database: %w", err)
		}
		out = append(out, db)
	}
	return out, nil
}

// GetDatabase fetches a database by ID.
func (c *Client) GetDatabase(ctx context.Context, id string) (*Database, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("database id is required")
	}
	var db Database
	if err := c.Do(ctx, "GET", "/databases/"+url.PathEscape(id), nil, &db); err != nil {
		return nil, err
	}
	return &db, nil
}

// QueryDatabaseOptions captures the subset of `POST /v1/databases/{id}/query`
// we exercise. Filter and Sorts are passed through as-is — Notion's
// filter language is too rich to type cleanly here; callers should
// construct map[string]any per the upstream docs.
type QueryDatabaseOptions struct {
	Filter      map[string]any   // upstream filter object (Notion's spec)
	Sorts       []map[string]any // upstream sorts array
	StartCursor string
	PageSize    int
}

// QueryDatabase executes a typed query against a database. Each row in
// the result is a Page (Notion models DB rows as pages).
func (c *Client) QueryDatabase(ctx context.Context, id string, opts QueryDatabaseOptions) ([]Page, string, bool, error) {
	if strings.TrimSpace(id) == "" {
		return nil, "", false, errors.New("database id is required")
	}
	body := map[string]any{}
	if opts.Filter != nil {
		body["filter"] = opts.Filter
	}
	if len(opts.Sorts) > 0 {
		body["sorts"] = opts.Sorts
	}
	if opts.StartCursor != "" {
		body["start_cursor"] = opts.StartCursor
	}
	if opts.PageSize > 0 {
		body["page_size"] = opts.PageSize
	}
	var resp PaginatedResponse
	if err := c.Do(ctx, "POST", "/databases/"+url.PathEscape(id)+"/query", body, &resp); err != nil {
		return nil, "", false, err
	}
	rows := make([]Page, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, "", false, fmt.Errorf("decode database row: %w", err)
		}
		rows = append(rows, p)
	}
	return rows, resp.NextCursor, resp.HasMore, nil
}

// CreateDatabaseRow is a thin wrapper over CreatePage with the parent
// type pinned to database_id. Kept as a named API because the agent
// surfaces "create row" rather than "create page with database parent".
func (c *Client) CreateDatabaseRow(ctx context.Context, databaseID string, properties map[string]any) (*Page, error) {
	return c.CreatePage(ctx, ParentTypeDatabase, databaseID, properties, nil)
}

// TitleOfDatabase joins the database's title rich-text spans.
func TitleOfDatabase(db *Database) string {
	if db == nil {
		return ""
	}
	return joinRichText(db.Title)
}
