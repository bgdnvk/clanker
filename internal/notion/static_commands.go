package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// CreateNotionCommands builds the `clanker notion` command tree. The ask
// subcommand is wired up by cmd/notion.go so this package stays free of
// the AI provider dependencies.
func CreateNotionCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "notion",
		Short:   "Query and write to your Notion workspace",
		Long:    "Browse pages, query databases, append blocks, and manage Notion content directly.",
		Aliases: []string{"nt"},
	}

	cmd.PersistentFlags().String("token", "", "Notion integration token (overrides config)")
	cmd.PersistentFlags().String("database", "", "Default database id (overrides config)")
	cmd.PersistentFlags().String("format", "table", "Output format: table | json")
	cmd.PersistentFlags().Bool("debug", false, "Enable debug output")

	cmd.AddCommand(buildListCommand())
	cmd.AddCommand(buildGetCommand())
	cmd.AddCommand(buildSearchCommand())
	cmd.AddCommand(buildPageCommand())
	cmd.AddCommand(buildDBCommand())

	return cmd
}

func clientFromCmd(cmd *cobra.Command) (*Client, error) {
	token, _ := cmd.Flags().GetString("token")
	if token == "" {
		token = ResolveToken()
	}
	if token == "" {
		return nil, fmt.Errorf("notion integration token is required (set --token, NOTION_API_KEY, or notion.integration_token)")
	}
	db, _ := cmd.Flags().GetString("database")
	if db == "" {
		db = ResolveDefaultDatabaseID()
	}
	debug, _ := cmd.Flags().GetBool("debug")
	return NewClient(token, db, debug)
}

func outputFormat(cmd *cobra.Command) string {
	f, _ := cmd.Flags().GetString("format")
	if f == "json" {
		return "json"
	}
	return "table"
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func buildListCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "list <resource>",
		Short: "List Notion resources",
		Long: `List Notion resources of a specific type.

Supported resources:
  pages       - Top search results across the workspace (filter by --query)
  databases   - Databases the integration can access
  users       - Workspace users (people + integration bots)`,
		Args: cobra.ExactArgs(1),
		RunE: runList,
	}
	c.Flags().String("query", "", "Free-text query (matches titles, NOT block content)")
	c.Flags().Int("limit", 25, "Page size (1..100)")
	return c
}

func runList(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	q, _ := cmd.Flags().GetString("query")
	limit, _ := cmd.Flags().GetInt("limit")

	switch strings.ToLower(args[0]) {
	case "pages", "page":
		pages, err := client.SearchPages(ctx, q, limit)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(pages)
		}
		return renderPages(pages)
	case "databases", "database", "dbs", "db":
		dbs, err := client.ListDatabases(ctx, q, limit)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(dbs)
		}
		return renderDatabases(dbs)
	case "users", "user":
		users, err := client.ListUsers(ctx, limit)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(users)
		}
		return renderUsers(users)
	default:
		return fmt.Errorf("unknown resource %q (supported: pages, databases, users)", args[0])
	}
}

func renderPages(pages []Page) error {
	w := newTabWriter()
	fmt.Fprintln(w, "ID\tTITLE\tPARENT\tEDITED")
	for _, p := range pages {
		parent := p.Parent.Type
		switch p.Parent.Type {
		case ParentTypeDatabase:
			parent = "db:" + shorten(p.Parent.DatabaseID)
		case ParentTypePage:
			parent = "page:" + shorten(p.Parent.PageID)
		case ParentTypeWorkspace:
			parent = "workspace"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shorten(p.ID), TitleOfPage(&p), parent, p.LastEditedTime.Format(time.RFC3339))
	}
	return w.Flush()
}

func renderDatabases(dbs []Database) error {
	w := newTabWriter()
	fmt.Fprintln(w, "ID\tTITLE\tEDITED")
	for _, db := range dbs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", shorten(db.ID), TitleOfDatabase(&db), db.LastEditedTime.Format(time.RFC3339))
	}
	return w.Flush()
}

func renderUsers(users []User) error {
	w := newTabWriter()
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tEMAIL")
	for _, u := range users {
		email := ""
		if u.Person != nil {
			email = u.Person.Email
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shorten(u.ID), u.Name, u.Type, email)
	}
	return w.Flush()
}

func shorten(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…"
}

func buildGetCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "get <resource> <id>",
		Short: "Get a single Notion resource",
		Long: `Get a single Notion resource by ID.

Examples:
  clanker notion get page <page-id>
  clanker notion get database <db-id>
  clanker notion get blocks <page-id>   # recursive block tree (capped)`,
		Args: cobra.ExactArgs(2),
		RunE: runGet,
	}
	return c
}

func runGet(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	resource := strings.ToLower(args[0])
	id := args[1]
	switch resource {
	case "page":
		p, err := client.GetPage(ctx, id)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(p)
		}
		fmt.Printf("ID: %s\nTitle: %s\nURL: %s\nEdited: %s\n", p.ID, TitleOfPage(p), p.URL, p.LastEditedTime.Format(time.RFC3339))
		return nil
	case "database", "db":
		db, err := client.GetDatabase(ctx, id)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(db)
		}
		fmt.Printf("ID: %s\nTitle: %s\nURL: %s\n", db.ID, TitleOfDatabase(db), db.URL)
		return nil
	case "blocks", "block":
		tree, count, err := client.GetPageBlocks(ctx, id)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return writeJSON(tree)
		}
		fmt.Println(BlocksToMarkdown(tree))
		fmt.Fprintf(os.Stderr, "(%d blocks)\n", count)
		return nil
	}
	return fmt.Errorf("unknown resource %q (supported: page, database, blocks)", resource)
}

func buildSearchCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-title search across the workspace",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			q := strings.Join(args, " ")
			pages, err := client.SearchPages(ctx, q, 25)
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return writeJSON(pages)
			}
			if len(pages) == 0 {
				fmt.Fprintln(os.Stderr, "no results — has the integration been shared with the relevant pages?")
				return nil
			}
			return renderPages(pages)
		},
	}
	return c
}

func buildPageCommand() *cobra.Command {
	page := &cobra.Command{
		Use:   "page <create|append>",
		Short: "Create pages and append blocks",
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a page under a parent page or database",
		RunE:  runPageCreate,
	}
	create.Flags().String("parent", "", "Parent page or database id (required)")
	create.Flags().String("parent-type", "page_id", "Parent type: page_id | database_id")
	create.Flags().String("title", "", "Page title")
	create.Flags().String("markdown", "", "Path to a markdown file with the page body")
	create.Flags().String("text", "", "Inline markdown body (alternative to --markdown)")
	_ = create.MarkFlagRequired("parent")
	_ = create.MarkFlagRequired("title")

	append := &cobra.Command{
		Use:   "append",
		Short: "Append blocks to an existing page",
		RunE:  runPageAppend,
	}
	append.Flags().String("page", "", "Target page id (required)")
	append.Flags().String("markdown", "", "Path to a markdown file with the content to append")
	append.Flags().String("text", "", "Inline markdown content to append")
	_ = append.MarkFlagRequired("page")

	page.AddCommand(create, append)
	return page
}

func runPageCreate(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	parent, _ := cmd.Flags().GetString("parent")
	parentType, _ := cmd.Flags().GetString("parent-type")
	title, _ := cmd.Flags().GetString("title")
	mdPath, _ := cmd.Flags().GetString("markdown")
	mdText, _ := cmd.Flags().GetString("text")

	md, err := readMarkdownInput(mdPath, mdText)
	if err != nil {
		return err
	}
	children := MarkdownToBlocks(md)

	// Both database rows and free-form pages use a "title" property; for
	// database parents the user must ensure that column exists.
	properties := TitleProperty(title)

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	p, err := client.CreatePage(ctx, parentType, parent, properties, children)
	if err != nil {
		return err
	}
	if outputFormat(cmd) == "json" {
		return writeJSON(p)
	}
	fmt.Printf("Created %s\n%s\n", p.ID, p.URL)
	return nil
}

func runPageAppend(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	pageID, _ := cmd.Flags().GetString("page")
	mdPath, _ := cmd.Flags().GetString("markdown")
	mdText, _ := cmd.Flags().GetString("text")

	md, err := readMarkdownInput(mdPath, mdText)
	if err != nil {
		return err
	}
	children := MarkdownToBlocks(md)
	if len(children) == 0 {
		return fmt.Errorf("no blocks parsed from input")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	appended, err := client.AppendBlockChildren(ctx, pageID, children)
	if err != nil {
		return err
	}
	if outputFormat(cmd) == "json" {
		return writeJSON(appended)
	}
	fmt.Printf("Appended %d blocks to %s\n", len(appended), pageID)
	return nil
}

func readMarkdownInput(path, inline string) (string, error) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read markdown: %w", err)
		}
		return string(raw), nil
	}
	if inline != "" {
		return inline, nil
	}
	return "", fmt.Errorf("either --markdown or --text is required")
}

func buildDBCommand() *cobra.Command {
	db := &cobra.Command{
		Use:   "db <subcommand>",
		Short: "Database row operations",
	}
	row := &cobra.Command{
		Use:   "row <create|update>",
		Short: "Create or update database rows",
	}
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a row in a database (properties supplied as JSON)",
		RunE:  runRowCreate,
	}
	create.Flags().String("db", "", "Database id (defaults to notion.default_database_id)")
	create.Flags().String("json", "", "Properties payload (Notion's typed property shape)")
	_ = create.MarkFlagRequired("json")

	update := &cobra.Command{
		Use:   "update",
		Short: "Patch a row's properties",
		RunE:  runRowUpdate,
	}
	update.Flags().String("row", "", "Row (page) id")
	update.Flags().String("json", "", "Properties patch")
	_ = update.MarkFlagRequired("row")
	_ = update.MarkFlagRequired("json")

	row.AddCommand(create, update)
	db.AddCommand(row)
	return db
}

func runRowCreate(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	dbID, _ := cmd.Flags().GetString("db")
	if dbID == "" {
		dbID = client.DefaultDatabaseID()
	}
	if dbID == "" {
		return fmt.Errorf("database id is required (--db or notion.default_database_id)")
	}
	raw, _ := cmd.Flags().GetString("json")
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return fmt.Errorf("parse properties JSON: %w", err)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	p, err := client.CreateDatabaseRow(ctx, dbID, props)
	if err != nil {
		return err
	}
	if outputFormat(cmd) == "json" {
		return writeJSON(p)
	}
	fmt.Printf("Created row %s\n%s\n", p.ID, p.URL)
	return nil
}

func runRowUpdate(cmd *cobra.Command, args []string) error {
	client, err := clientFromCmd(cmd)
	if err != nil {
		return err
	}
	rowID, _ := cmd.Flags().GetString("row")
	raw, _ := cmd.Flags().GetString("json")
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return fmt.Errorf("parse properties JSON: %w", err)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	p, err := client.UpdatePageProperties(ctx, rowID, props)
	if err != nil {
		return err
	}
	if outputFormat(cmd) == "json" {
		return writeJSON(p)
	}
	fmt.Printf("Updated row %s\n", p.ID)
	return nil
}
