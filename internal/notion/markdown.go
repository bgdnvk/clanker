package notion

import (
	"strings"
)

// MarkdownToBlocks converts a minimal subset of CommonMark into the
// block array Notion expects on `PATCH /v1/blocks/{id}/children`.
//
// Supported syntax (intentionally narrow — the use case is "agent
// emits markdown, we round-trip it into Notion blocks", not full
// CommonMark fidelity):
//
//   - `# h1`, `## h2`, `### h3` (h4+ collapse to h3 since Notion only
//     has three heading levels)
//   - `- item` and `* item` → bulleted list item
//   - `1. item` (and any digit prefix + `.`) → numbered list item
//   - `- [ ] todo` / `- [x] done` → to-do block
//   - `> quote` → quote block
//   - ```` ```lang ```` fenced code blocks (single-line `lang` opener)
//   - `---` / `***` (line of three or more) → divider
//   - blank lines → paragraph breaks
//   - everything else → paragraph
//
// Inline annotations (bold/italic/code/links) are NOT preserved — the
// whole line becomes plain text. Worth revisiting once an agent
// actually needs bold output; until then, paragraph + plain text keeps
// the converter trivial and lossless to round-trip back to text.
func MarkdownToBlocks(md string) []map[string]any {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	blocks := []map[string]any{}
	var paragraph strings.Builder

	flushParagraph := func() {
		text := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if text != "" {
			blocks = append(blocks, ParagraphBlock(text))
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			flushParagraph()
			continue
		}

		// Fenced code: collect until matching close.
		if strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			var code strings.Builder
			j := i + 1
			for ; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
					break
				}
				if code.Len() > 0 {
					code.WriteByte('\n')
				}
				code.WriteString(lines[j])
			}
			blocks = append(blocks, CodeBlock(lang, code.String()))
			i = j
			continue
		}

		if isDividerLine(trimmed) {
			flushParagraph()
			blocks = append(blocks, DividerBlock())
			continue
		}

		if level, rest, ok := matchHeading(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, HeadingBlock(level, rest))
			continue
		}

		if rest, checked, ok := matchTodo(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, ToDoBlock(rest, checked))
			continue
		}

		if rest, ok := matchBullet(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, BulletedListItemBlock(rest))
			continue
		}

		if rest, ok := matchNumbered(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, NumberedListItemBlock(rest))
			continue
		}

		if strings.HasPrefix(trimmed, "> ") {
			flushParagraph()
			blocks = append(blocks, QuoteBlock(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))))
			continue
		}

		if paragraph.Len() > 0 {
			paragraph.WriteByte('\n')
		}
		paragraph.WriteString(trimmed)
	}
	flushParagraph()
	return blocks
}

func matchHeading(s string) (int, string, bool) {
	hashes := 0
	for hashes < len(s) && s[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes >= len(s) || s[hashes] != ' ' {
		return 0, "", false
	}
	// Notion has only three heading levels; H4+ collapse to H3.
	level := min(hashes, 3)
	return level, strings.TrimSpace(s[hashes+1:]), true
}

func matchBullet(s string) (string, bool) {
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(s[len(p):]), true
		}
	}
	return "", false
}

func matchNumbered(s string) (string, bool) {
	// e.g. "1. foo", "12. bar"
	idx := strings.Index(s, ". ")
	if idx <= 0 || idx > 4 {
		return "", false
	}
	for i := range idx {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	return strings.TrimSpace(s[idx+2:]), true
}

func matchTodo(s string) (string, bool, bool) {
	// "- [ ] foo" or "- [x] foo" / "* [X] foo" — handle both the dash
	// and asterisk prefixes plus upper/lower-case x.
	for _, p := range []string{"- ", "* ", "+ "} {
		if !strings.HasPrefix(s, p) {
			continue
		}
		rest := s[len(p):]
		if len(rest) < 3 || rest[0] != '[' || rest[2] != ']' {
			continue
		}
		marker := rest[1]
		if marker != ' ' && marker != 'x' && marker != 'X' {
			continue
		}
		body := strings.TrimSpace(rest[3:])
		return body, marker == 'x' || marker == 'X', true
	}
	return "", false, false
}

func isDividerLine(s string) bool {
	if len(s) < 3 {
		return false
	}
	if !(s[0] == '-' || s[0] == '*' || s[0] == '_') {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// BlocksToMarkdown renders a Notion block tree back to a markdown string.
// Used by the desktop "doc viewer" and by `clanker notion get page` to
// give operators a copy-pasteable form. Round-trips the subset that
// MarkdownToBlocks emits; richer block types render to a best-effort
// approximation (callouts as quotes, child pages as placeholders).
func BlocksToMarkdown(blocks []PageBlockTree) string {
	var sb strings.Builder
	renderBlocks(&sb, blocks, 0)
	return sb.String()
}

func renderBlocks(sb *strings.Builder, nodes []PageBlockTree, indent int) {
	ind := strings.Repeat("  ", indent)
	for _, n := range nodes {
		text := (&n.Block).RichTextPlain()
		switch n.Type {
		case "heading_1":
			sb.WriteString("# ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "heading_2":
			sb.WriteString("## ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "heading_3":
			sb.WriteString("### ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "bulleted_list_item":
			sb.WriteString(ind)
			sb.WriteString("- ")
			sb.WriteString(text)
			sb.WriteByte('\n')
		case "numbered_list_item":
			sb.WriteString(ind)
			sb.WriteString("1. ")
			sb.WriteString(text)
			sb.WriteByte('\n')
		case "to_do":
			sb.WriteString(ind)
			sb.WriteString("- [ ] ")
			sb.WriteString(text)
			sb.WriteByte('\n')
		case "quote":
			sb.WriteString("> ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "code":
			sb.WriteString("```\n")
			sb.WriteString(text)
			sb.WriteString("\n```\n\n")
		case "divider":
			sb.WriteString("---\n\n")
		case "child_page":
			sb.WriteString("[child page]\n\n")
		case "child_database":
			sb.WriteString("[child database]\n\n")
		default:
			if text != "" {
				sb.WriteString(text)
				sb.WriteString("\n\n")
			}
		}
		if len(n.Children) > 0 {
			renderBlocks(sb, n.Children, indent+1)
		}
	}
}
