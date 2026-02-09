package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CFInfraSnapshot holds existing Cloudflare resources
type CFInfraSnapshot struct {
	AccountID     string   `json:"accountId,omitempty"`
	Workers       []string `json:"workers,omitempty"`       // existing worker names
	PagesProjects []string `json:"pagesProjects,omitempty"` // existing pages projects
	KVNamespaces  []string `json:"kvNamespaces,omitempty"`  // existing KV namespaces
	D1Databases   []string `json:"d1Databases,omitempty"`   // existing D1 databases
	R2Buckets     []string `json:"r2Buckets,omitempty"`     // existing R2 buckets
}

// ScanCFInfra queries the Cloudflare account for existing resources via wrangler
func ScanCFInfra(ctx context.Context, logf func(string, ...any)) *CFInfraSnapshot {
	snap := &CFInfraSnapshot{}

	// whoami — get account info
	if out := wranglerCLI(ctx, "whoami"); out != "" {
		snap.AccountID = strings.TrimSpace(out)
	}

	// list pages projects
	if out := wranglerCLI(ctx, "pages", "project", "list"); out != "" {
		snap.PagesProjects = parseCFListOutput(out)
	}

	// list KV namespaces
	if out := wranglerCLI(ctx, "kv", "namespace", "list"); out != "" {
		snap.KVNamespaces = parseJSONNames(out, "title")
	}

	// list D1 databases
	if out := wranglerCLI(ctx, "d1", "list"); out != "" {
		snap.D1Databases = parseJSONNames(out, "name")
	}

	// list R2 buckets
	if out := wranglerCLI(ctx, "r2", "bucket", "list"); out != "" {
		snap.R2Buckets = parseJSONNames(out, "name")
	}

	logf("[cf-scan] found: %d pages projects, %d KV namespaces, %d D1 databases, %d R2 buckets",
		len(snap.PagesProjects), len(snap.KVNamespaces), len(snap.D1Databases), len(snap.R2Buckets))

	return snap
}

// FormatCFForPrompt formats the CF infra snapshot for LLM context
func (s *CFInfraSnapshot) FormatCFForPrompt() string {
	if s == nil {
		return ""
	}

	var b strings.Builder

	if len(s.PagesProjects) > 0 {
		b.WriteString("Existing Pages Projects: " + strings.Join(s.PagesProjects, ", ") + "\n")
	}
	if len(s.KVNamespaces) > 0 {
		b.WriteString("Existing KV Namespaces: " + strings.Join(s.KVNamespaces, ", ") + "\n")
	}
	if len(s.D1Databases) > 0 {
		b.WriteString("Existing D1 Databases: " + strings.Join(s.D1Databases, ", ") + "\n")
	}
	if len(s.R2Buckets) > 0 {
		b.WriteString("Existing R2 Buckets: " + strings.Join(s.R2Buckets, ", ") + "\n")
	}

	return b.String()
}

// wranglerCLI runs a wrangler command, returns stdout or empty on error
func wranglerCLI(ctx context.Context, args ...string) string {
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(tctx, "npx", append([]string{"wrangler"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

// parseCFListOutput parses wrangler list output (table format) into names
func parseCFListOutput(raw string) []string {
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "│") || strings.HasPrefix(line, "┌") || strings.HasPrefix(line, "└") || strings.HasPrefix(line, "├") {
			// try to extract name from table row
			if strings.Contains(line, "│") {
				cols := strings.Split(line, "│")
				if len(cols) >= 2 {
					name := strings.TrimSpace(cols[1])
					if name != "" && name != "Name" && name != "name" {
						names = append(names, name)
					}
				}
			}
		}
	}
	return names
}

// parseJSONNames parses wrangler JSON list output into name strings
func parseJSONNames(raw string, nameKey string) []string {
	// wrangler outputs JSON arrays for --json but sometimes plain JSON
	raw = strings.TrimSpace(raw)

	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		// try to find JSON array in the output
		start := strings.Index(raw, "[")
		end := strings.LastIndex(raw, "]")
		if start >= 0 && end > start {
			if err := json.Unmarshal([]byte(raw[start:end+1]), &items); err != nil {
				return nil
			}
		} else {
			return nil
		}
	}

	var names []string
	for _, item := range items {
		if name, ok := item[nameKey]; ok {
			names = append(names, fmt.Sprintf("%v", name))
		}
	}
	return names
}
