package notion

import (
	"encoding/json"
	"time"
)

// Notion API responses use snake_case keys and ISO-8601 timestamp strings.
// The block tree is polymorphic — each Block carries a Type discriminator
// plus a typed-per-Type body; we keep the typed payload as RawMessage and
// let callers dispatch on Type.

// Workspace identifies the integration's accessible scope. Notion's API
// does not expose a "current workspace" endpoint — we infer name/id from
// `GET /v1/users/me` → `bot.workspace_name`.
type Workspace struct {
	BotID         string `json:"bot_id"`
	WorkspaceName string `json:"workspace_name"`
}

// User is a workspace member (`type=person`) or an integration bot
// (`type=bot`). Both share Name + AvatarURL; Person carries Email.
type User struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "person" | "bot"
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Person    *struct {
		Email string `json:"email"`
	} `json:"person,omitempty"`
	Bot *struct {
		Owner         json.RawMessage `json:"owner,omitempty"`
		WorkspaceName string          `json:"workspace_name,omitempty"`
	} `json:"bot,omitempty"`
}

// ParentType values for CreatePage and friends. Kept as typed string so
// callers get a compile-time error on typos (Notion silently 400s on
// unknown parent types).
const (
	ParentTypePage      = "page_id"
	ParentTypeDatabase  = "database_id"
	ParentTypeWorkspace = "workspace"
	ParentTypeBlock     = "block_id"
)

// FilterObject values for SearchOptions.FilterObject — restricts the
// `POST /v1/search` result set to one object kind.
const (
	FilterObjectPage     = "page"
	FilterObjectDatabase = "database"
)

// Parent identifies the owner of a Page/Database/Block. Exactly one of
// the fields is populated based on Type ("database_id" | "page_id" |
// "workspace" | "block_id").
type Parent struct {
	Type       string `json:"type"`
	DatabaseID string `json:"database_id,omitempty"`
	PageID     string `json:"page_id,omitempty"`
	BlockID    string `json:"block_id,omitempty"`
	Workspace  bool   `json:"workspace,omitempty"`
}

// Page can be a free-form document OR a database row — the discriminator
// is Parent.Type. Database rows have typed Properties; free-form pages
// only have a `title` property.
type Page struct {
	Object         string                     `json:"object"` // always "page"
	ID             string                     `json:"id"`
	CreatedTime    time.Time                  `json:"created_time"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	Archived       bool                       `json:"archived"`
	URL            string                     `json:"url"`
	Parent         Parent                     `json:"parent"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Icon           json.RawMessage            `json:"icon,omitempty"`
	Cover          json.RawMessage            `json:"cover,omitempty"`
}

// Database is a Notion table. Properties define the schema (column types).
type Database struct {
	Object         string                     `json:"object"` // always "database"
	ID             string                     `json:"id"`
	CreatedTime    time.Time                  `json:"created_time"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	Archived       bool                       `json:"archived"`
	URL            string                     `json:"url"`
	Title          []RichTextSpan             `json:"title"`
	Description    []RichTextSpan             `json:"description"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Parent         Parent                     `json:"parent"`
}

// Block is one node in a page's content tree. The polymorphic payload
// lives in the field named after Type (e.g. Type="paragraph" → field
// `paragraph: {rich_text, color}`). We unmarshal everything into
// RawMessage and dispatch lazily.
type Block struct {
	Object         string    `json:"object"` // always "block"
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	HasChildren    bool      `json:"has_children"`
	Archived       bool      `json:"archived"`
	CreatedTime    time.Time `json:"created_time"`
	LastEditedTime time.Time `json:"last_edited_time"`
	Parent         Parent    `json:"parent"`

	// The per-type payload sits at top level under a key matching Type.
	// We capture the whole envelope so block renderers can pull any
	// fields they need without re-defining every variant struct here.
	Paragraph        json.RawMessage `json:"paragraph,omitempty"`
	Heading1         json.RawMessage `json:"heading_1,omitempty"`
	Heading2         json.RawMessage `json:"heading_2,omitempty"`
	Heading3         json.RawMessage `json:"heading_3,omitempty"`
	BulletedListItem json.RawMessage `json:"bulleted_list_item,omitempty"`
	NumberedListItem json.RawMessage `json:"numbered_list_item,omitempty"`
	ToDo             json.RawMessage `json:"to_do,omitempty"`
	Code             json.RawMessage `json:"code,omitempty"`
	Quote            json.RawMessage `json:"quote,omitempty"`
	Callout          json.RawMessage `json:"callout,omitempty"`
	Divider          json.RawMessage `json:"divider,omitempty"`
	Image            json.RawMessage `json:"image,omitempty"`
	ChildPage        json.RawMessage `json:"child_page,omitempty"`
	ChildDatabase    json.RawMessage `json:"child_database,omitempty"`
}

// RichTextSpan is the atom inside a rich-text array. Notion uses these
// for prose with inline annotations (bold/italic/code/links). For most
// machine-rendered prose we only care about PlainText.
type RichTextSpan struct {
	Type        string `json:"type"`
	PlainText   string `json:"plain_text"`
	Href        string `json:"href,omitempty"`
	Annotations struct {
		Bold          bool   `json:"bold"`
		Italic        bool   `json:"italic"`
		Strikethrough bool   `json:"strikethrough"`
		Underline     bool   `json:"underline"`
		Code          bool   `json:"code"`
		Color         string `json:"color"`
	} `json:"annotations"`
}

// PaginatedResponse is Notion's standard list envelope. NextCursor is
// the opaque string to pass back on the next call; HasMore signals when
// to stop. Results carries the typed array (we use json.RawMessage to
// avoid coupling list operations to a specific result type).
type PaginatedResponse struct {
	Object     string            `json:"object"` // always "list"
	Results    []json.RawMessage `json:"results"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

// AccountStatus is the conversation-history snapshot for `notion ask`.
// "Accessible" is intentional: pages stay at zero until the user shares
// them with the integration, surfacing the #1 Notion UX papercut.
type AccountStatus struct {
	Timestamp       time.Time `json:"timestamp"`
	WorkspaceName   string    `json:"workspace_name,omitempty"`
	AccessiblePages int       `json:"accessible_pages"`
	DatabaseCount   int       `json:"database_count"`
}
