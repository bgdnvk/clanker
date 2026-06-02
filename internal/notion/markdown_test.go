package notion

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownToBlocks_Paragraphs(t *testing.T) {
	blocks := MarkdownToBlocks("first paragraph\nstill first\n\nsecond paragraph")
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[0]["type"] != "paragraph" || blocks[1]["type"] != "paragraph" {
		t.Errorf("unexpected types: %v / %v", blocks[0]["type"], blocks[1]["type"])
	}
}

func TestMarkdownToBlocks_Headings(t *testing.T) {
	blocks := MarkdownToBlocks("# H1\n## H2\n### H3\n#### deeper")
	if len(blocks) != 4 {
		t.Fatalf("want 4 blocks, got %d", len(blocks))
	}
	want := []string{"heading_1", "heading_2", "heading_3", "heading_3"}
	for i, b := range blocks {
		if b["type"] != want[i] {
			t.Errorf("block[%d] type: got %v, want %s", i, b["type"], want[i])
		}
	}
}

func TestMarkdownToBlocks_Lists(t *testing.T) {
	blocks := MarkdownToBlocks("- bullet 1\n- bullet 2\n\n1. one\n2. two")
	if len(blocks) != 4 {
		t.Fatalf("want 4 blocks, got %d", len(blocks))
	}
	want := []string{"bulleted_list_item", "bulleted_list_item", "numbered_list_item", "numbered_list_item"}
	for i, b := range blocks {
		if b["type"] != want[i] {
			t.Errorf("block[%d] type: got %v, want %s", i, b["type"], want[i])
		}
	}
}

func TestMarkdownToBlocks_Todos(t *testing.T) {
	blocks := MarkdownToBlocks("- [ ] open\n- [x] done\n* [X] also done")
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	checked := []bool{false, true, true}
	for i, b := range blocks {
		if b["type"] != "to_do" {
			t.Errorf("block[%d] type: got %v", i, b["type"])
		}
		todo := b["to_do"].(map[string]any)
		if todo["checked"].(bool) != checked[i] {
			t.Errorf("block[%d] checked: got %v, want %v", i, todo["checked"], checked[i])
		}
	}
}

func TestMarkdownToBlocks_CodeFence(t *testing.T) {
	md := "before\n\n```go\nfunc x() {}\n```\n\nafter"
	blocks := MarkdownToBlocks(md)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks (para + code + para), got %d", len(blocks))
	}
	if blocks[1]["type"] != "code" {
		t.Fatalf("middle block should be code, got %v", blocks[1]["type"])
	}
	code := blocks[1]["code"].(map[string]any)
	if code["language"] != "go" {
		t.Errorf("language: got %v, want go", code["language"])
	}
	rt := code["rich_text"].([]map[string]any)
	if len(rt) == 0 {
		t.Fatal("code rich_text is empty")
	}
	body := rt[0]["text"].(map[string]any)["content"].(string)
	if !strings.Contains(body, "func x()") {
		t.Errorf("code body: %q", body)
	}
}

func TestMarkdownToBlocks_Divider(t *testing.T) {
	blocks := MarkdownToBlocks("above\n\n---\n\nbelow")
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	if blocks[1]["type"] != "divider" {
		t.Errorf("middle should be divider, got %v", blocks[1]["type"])
	}
}

func TestMarkdownToBlocks_QuoteAndEmpty(t *testing.T) {
	blocks := MarkdownToBlocks("> a quote\n\n")
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "quote" {
		t.Errorf("got %v, want quote", blocks[0]["type"])
	}
}

func TestRichTextChunk_LongInputSplits(t *testing.T) {
	long := strings.Repeat("a", 4500)
	chunks := richTextChunk(long)
	if len(chunks) != 3 {
		t.Errorf("want 3 chunks for 4500 chars, got %d", len(chunks))
	}
	for _, c := range chunks {
		txt := c["text"].(map[string]any)["content"].(string)
		if len(txt) > 2000 {
			t.Errorf("chunk exceeds 2000 chars: %d", len(txt))
		}
	}
}

func TestBlocksToMarkdown_RoundTripSubset(t *testing.T) {
	// Build a synthetic block tree that mimics what GetPageBlocks returns,
	// then render to markdown and verify the structure (we don't require
	// byte-identical round-trip — the converter explicitly drops inline
	// annotations).
	mkRichText := func(s string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"rich_text": []map[string]any{{"plain_text": s}},
		})
		return raw
	}
	tree := []PageBlockTree{
		{Block: Block{Type: "heading_1", Heading1: mkRichText("Title")}},
		{Block: Block{Type: "paragraph", Paragraph: mkRichText("body para")}},
		{Block: Block{Type: "bulleted_list_item", BulletedListItem: mkRichText("first")}},
		{Block: Block{Type: "bulleted_list_item", BulletedListItem: mkRichText("second")}},
		{Block: Block{Type: "divider"}},
		{Block: Block{Type: "code", Code: mkRichText("echo hi")}},
	}
	md := BlocksToMarkdown(tree)
	for _, must := range []string{"# Title", "body para", "- first", "- second", "---", "```", "echo hi"} {
		if !strings.Contains(md, must) {
			t.Errorf("rendered markdown missing %q. got:\n%s", must, md)
		}
	}
}

func TestPageBlockTree_RichTextPlain(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"rich_text": []map[string]any{
			{"plain_text": "hello "},
			{"plain_text": "world"},
		},
	})
	b := &Block{Type: "paragraph", Paragraph: raw}
	if got := b.RichTextPlain(); got != "hello world" {
		t.Errorf("RichTextPlain: got %q", got)
	}

	if (&Block{Type: "divider"}).RichTextPlain() != "" {
		t.Error("divider should render to empty plain text")
	}
}

func TestParagraphBlock_Shape(t *testing.T) {
	got := ParagraphBlock("hello")
	want := map[string]any{
		"object": "block",
		"type":   "paragraph",
		"paragraph": map[string]any{
			"rich_text": []map[string]any{{
				"type": "text",
				"text": map[string]any{"content": "hello"},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParagraphBlock shape mismatch.\ngot:  %#v\nwant: %#v", got, want)
	}
}
