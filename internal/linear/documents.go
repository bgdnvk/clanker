package linear

import (
	"context"
	"fmt"
)

const documentSelection = `
  id
  title
  url
  content
  createdAt
  updatedAt
  project { id }
`

const queryDocuments = `
query Documents($first: Int!) {
  documents(first: $first) {
    nodes { ` + documentSelection + ` }
  }
}`

const queryDocument = `
query Document($id: String!) {
  document(id: $id) { ` + documentSelection + ` }
}`

type docNode struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Content   any    `json:"content"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Project   *struct {
		ID string `json:"id"`
	} `json:"project"`
}

func (c *Client) ListDocuments(ctx context.Context) ([]Document, error) {
	var out struct {
		Documents struct {
			Nodes []docNode `json:"nodes"`
		} `json:"documents"`
	}
	if err := c.Do(ctx, queryDocuments, map[string]any{"first": 50}, &out); err != nil {
		return nil, err
	}
	docs := make([]Document, len(out.Documents.Nodes))
	for i, n := range out.Documents.Nodes {
		docs[i] = Document{
			ID:    n.ID,
			Title: n.Title,
			URL:   n.URL,
		}
		if n.Project != nil {
			docs[i].ProjectID = n.Project.ID
		}
	}
	return docs, nil
}

func (c *Client) GetDocument(ctx context.Context, id string) (*Document, error) {
	if id == "" {
		return nil, fmt.Errorf("document id required")
	}
	var out struct {
		Document *docNode `json:"document"`
	}
	if err := c.Do(ctx, queryDocument, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Document == nil {
		return nil, fmt.Errorf("document %s not found", id)
	}
	doc := Document{
		ID:    out.Document.ID,
		Title: out.Document.Title,
		URL:   out.Document.URL,
	}
	if out.Document.Project != nil {
		doc.ProjectID = out.Document.Project.ID
	}
	return &doc, nil
}
