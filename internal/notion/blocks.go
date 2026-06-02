package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Block-tree fetching is recursive. The walk is bounded to keep "get
// blocks for a huge page" calls predictable — Notion has no API-level
// safeguard against runaway depth.
const (
	maxBlockDepth      = 3
	maxBlocksPerPage   = 200
	getChildrenMaxSize = 100
)

// GetBlockChildren fetches one page (up to 100 nodes) of a block's
// direct children. Pagination is handled by GetBlockChildrenAll.
func (c *Client) GetBlockChildren(ctx context.Context, blockID, startCursor string, pageSize int) ([]Block, string, bool, error) {
	if strings.TrimSpace(blockID) == "" {
		return nil, "", false, errors.New("block id is required")
	}
	if pageSize <= 0 {
		pageSize = getChildrenMaxSize
	}
	pageSize = min(pageSize, getChildrenMaxSize)
	path := fmt.Sprintf("/blocks/%s/children?page_size=%d", url.PathEscape(blockID), pageSize)
	if startCursor != "" {
		path += "&start_cursor=" + url.QueryEscape(startCursor)
	}
	var resp PaginatedResponse
	if err := c.Do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, "", false, err
	}
	blocks := make([]Block, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var b Block
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, "", false, fmt.Errorf("decode block: %w", err)
		}
		blocks = append(blocks, b)
	}
	return blocks, resp.NextCursor, resp.HasMore, nil
}

// GetBlockChildrenAll paginates through every direct child of a block.
// Bounded by maxBlocksPerPage so a single huge page can't fan into 100+
// sequential requests at Notion's 3 req/s ceiling. Returns partial
// results when the cap is reached.
func (c *Client) GetBlockChildrenAll(ctx context.Context, blockID string) ([]Block, error) {
	var all []Block
	cursor := ""
	for {
		page, next, more, err := c.GetBlockChildren(ctx, blockID, cursor, 0)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(all) >= maxBlocksPerPage {
			if len(all) > maxBlocksPerPage {
				all = all[:maxBlocksPerPage]
			}
			break
		}
		if !more || next == "" {
			break
		}
		cursor = next
	}
	return all, nil
}

// PageBlockTree is one node in the recursively-fetched tree. Depth=0 is
// the top-level page; child blocks are nested via Children.
type PageBlockTree struct {
	Block
	Depth    int             `json:"depth"`
	Children []PageBlockTree `json:"children,omitempty"`
	// Truncated is true when we stopped descending (depth cap, count cap,
	// or fetch error). UI surfaces this so users know to load more.
	Truncated bool `json:"truncated,omitempty"`
}

// GetPageBlocks fetches a page's block tree recursively. Bounded by
// maxBlockDepth + maxBlocksPerPage to keep latency + memory predictable.
// Returns partial results when the cap kicks in (Truncated=true on the
// node where descent stopped).
func (c *Client) GetPageBlocks(ctx context.Context, pageID string) ([]PageBlockTree, int, error) {
	count := 0
	trees, err := c.fetchTree(ctx, pageID, 0, &count)
	return trees, count, err
}

func (c *Client) fetchTree(ctx context.Context, parentID string, depth int, count *int) ([]PageBlockTree, error) {
	if *count >= maxBlocksPerPage {
		return nil, nil
	}
	children, err := c.GetBlockChildrenAll(ctx, parentID)
	if err != nil {
		return nil, err
	}
	trees := make([]PageBlockTree, 0, len(children))
	for _, b := range children {
		if *count >= maxBlocksPerPage {
			break
		}
		*count++
		node := PageBlockTree{Block: b, Depth: depth}
		if b.HasChildren {
			if depth+1 >= maxBlockDepth {
				node.Truncated = true
			} else {
				sub, subErr := c.fetchTree(ctx, b.ID, depth+1, count)
				if subErr != nil {
					node.Truncated = true
				} else {
					node.Children = sub
				}
			}
		}
		trees = append(trees, node)
	}
	return trees, nil
}

// AppendBlockChildren appends new children to a parent (page or block).
// Returns the appended blocks with their server-assigned IDs.
func (c *Client) AppendBlockChildren(ctx context.Context, parentID string, children []map[string]any) ([]Block, error) {
	if strings.TrimSpace(parentID) == "" {
		return nil, errors.New("parent id is required")
	}
	if len(children) == 0 {
		return nil, errors.New("at least one child block is required")
	}
	body := map[string]any{"children": children}
	var resp PaginatedResponse
	if err := c.Do(ctx, "PATCH", "/blocks/"+url.PathEscape(parentID)+"/children", body, &resp); err != nil {
		return nil, err
	}
	out := make([]Block, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var b Block
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("decode appended block: %w", err)
		}
		out = append(out, b)
	}
	return out, nil
}

// DeleteBlock soft-deletes (archives) a block.
func (c *Client) DeleteBlock(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("block id is required")
	}
	return c.Do(ctx, "DELETE", "/blocks/"+url.PathEscape(id), nil, nil)
}

// Block builders return map[string]any rather than concrete structs because
// Notion's payload is sparse — each block has a single populated key
// matching its type, and every other type key is absent.

func richTextChunk(text string) []map[string]any {
	if text == "" {
		return []map[string]any{}
	}
	// Notion caps a single rich_text item at 2000 characters; longer
	// content must be split into multiple spans of the same paragraph.
	const limit = 2000
	chunks := []map[string]any{}
	for len(text) > 0 {
		n := min(len(text), limit)
		chunks = append(chunks, map[string]any{
			"type": "text",
			"text": map[string]any{"content": text[:n]},
		})
		text = text[n:]
	}
	return chunks
}

func ParagraphBlock(text string) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "paragraph",
		"paragraph": map[string]any{
			"rich_text": richTextChunk(text),
		},
	}
}

func HeadingBlock(level int, text string) map[string]any {
	switch level {
	case 1, 2, 3:
	default:
		level = 1
	}
	t := fmt.Sprintf("heading_%d", level)
	return map[string]any{
		"object": "block",
		"type":   t,
		t: map[string]any{
			"rich_text": richTextChunk(text),
		},
	}
}

func BulletedListItemBlock(text string) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "bulleted_list_item",
		"bulleted_list_item": map[string]any{
			"rich_text": richTextChunk(text),
		},
	}
}

func NumberedListItemBlock(text string) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "numbered_list_item",
		"numbered_list_item": map[string]any{
			"rich_text": richTextChunk(text),
		},
	}
}

func ToDoBlock(text string, checked bool) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "to_do",
		"to_do": map[string]any{
			"rich_text": richTextChunk(text),
			"checked":   checked,
		},
	}
}

func QuoteBlock(text string) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "quote",
		"quote": map[string]any{
			"rich_text": richTextChunk(text),
		},
	}
}

func CodeBlock(language, text string) map[string]any {
	lang := strings.ToLower(strings.TrimSpace(language))
	if lang == "" {
		lang = "plain text"
	}
	return map[string]any{
		"object": "block",
		"type":   "code",
		"code": map[string]any{
			"rich_text": richTextChunk(text),
			"language":  lang,
		},
	}
}

func DividerBlock() map[string]any {
	return map[string]any{
		"object":  "block",
		"type":    "divider",
		"divider": map[string]any{},
	}
}

// RichTextPlain extracts plain text from a block's payload by reading
// the `rich_text` array under the type-named key. Returns empty string
// for non-textual blocks (divider, image, child_page, child_database).
func (b *Block) RichTextPlain() string {
	if b == nil {
		return ""
	}
	var payload json.RawMessage
	switch b.Type {
	case "paragraph":
		payload = b.Paragraph
	case "heading_1":
		payload = b.Heading1
	case "heading_2":
		payload = b.Heading2
	case "heading_3":
		payload = b.Heading3
	case "bulleted_list_item":
		payload = b.BulletedListItem
	case "numbered_list_item":
		payload = b.NumberedListItem
	case "to_do":
		payload = b.ToDo
	case "code":
		payload = b.Code
	case "quote":
		payload = b.Quote
	case "callout":
		payload = b.Callout
	}
	if len(payload) == 0 {
		return ""
	}
	var probe struct {
		RichText []RichTextSpan `json:"rich_text"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ""
	}
	return joinRichText(probe.RichText)
}
