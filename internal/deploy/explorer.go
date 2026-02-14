package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileRequest is what the LLM asks for during exploration
type FileRequest struct {
	Files    []string `json:"files"`    // files it wants to read
	Reason   string   `json:"reason"`   // why it needs them
	Done     bool     `json:"done"`     // true = has enough context
	Analysis string   `json:"analysis"` // partial analysis so far (when done=true)
}

// ExplorationResult is the output of the agentic exploration
type ExplorationResult struct {
	FilesRead map[string]string // all files read during exploration
	Rounds    int               // how many exploration rounds
	Analysis  string            // LLM's analysis after reading everything
}

const (
	maxExplorationRounds = 3    // max LLM→read→LLM loops
	maxFileSize          = 6144 // cap per file
	maxTotalFiles        = 20   // don't read the entire repo
)

// ExploreRepo runs agentic file exploration:
// LLM sees the tree → requests files → we read them → LLM requests more → done
func ExploreRepo(ctx context.Context, profile *RepoProfile, ask AskFunc, clean CleanFunc, logf func(string, ...any)) (*ExplorationResult, error) {
	result := &ExplorationResult{
		FilesRead: make(map[string]string),
	}

	// seed with key files already read by the static analyzer
	for name, content := range profile.KeyFiles {
		result.FilesRead[name] = content
	}

	// exploration loop
	for round := 0; round < maxExplorationRounds; round++ {
		result.Rounds = round + 1

		prompt := buildExplorationPrompt(profile, result.FilesRead, round)
		resp, err := ask(ctx, prompt)
		if err != nil {
			return result, fmt.Errorf("exploration round %d failed: %w", round, err)
		}

		req, err := parseFileRequest(clean(resp))
		if err != nil {
			logf("[explore] round %d: parse failed (%v), stopping", round, err)
			break
		}

		// LLM says it has enough context
		if req.Done {
			if req.Analysis != "" {
				result.Analysis = req.Analysis
			}
			logf("[explore] round %d: LLM satisfied (%d files read)", round, len(result.FilesRead))
			break
		}

		// read requested files
		newFiles := 0
		for _, f := range req.Files {
			if len(result.FilesRead) >= maxTotalFiles {
				break
			}
			if _, already := result.FilesRead[f]; already {
				continue
			}
			content := readRepoFile(profile.ClonePath, f)
			if content != "" {
				result.FilesRead[f] = content
				newFiles++
			}
		}

		logf("[explore] round %d: requested %d files, read %d new (%s)", round, len(req.Files), newFiles, req.Reason)

		// no new files = nothing left to explore
		if newFiles == 0 {
			break
		}
	}

	return result, nil
}

func buildExplorationPrompt(p *RepoProfile, filesRead map[string]string, round int) string {
	var b strings.Builder

	b.WriteString("You are analyzing a repository to understand how to build and deploy it.\n\n")

	// file tree
	b.WriteString("## Repository Structure\n```\n")
	b.WriteString(p.FileTree)
	b.WriteString("```\n\n")

	// files already read
	if len(filesRead) > 0 {
		b.WriteString("## Files Already Read\n")
		for name, content := range filesRead {
			b.WriteString(fmt.Sprintf("\n### %s\n```\n%s\n```\n", name, content))
		}
		b.WriteString("\n")
	}

	// static analysis
	b.WriteString(fmt.Sprintf("## Quick Facts\n- Language: %s\n- Framework: %s\n- Package manager: %s\n", p.Language, p.Framework, p.PackageManager))
	if p.IsMonorepo {
		b.WriteString("- Monorepo: yes\n")
	}
	if p.HasDocker {
		b.WriteString("- Has Dockerfile\n")
	}
	if p.HasCompose {
		b.WriteString("- Has docker-compose\n")
	}

	if round == 0 {
		b.WriteString(`
## Your Task
Look at the file tree and the files already read. Decide what OTHER files you need to read to fully understand:
1. How to BUILD this application
2. How to RUN it (locally and in production)
3. What services/components it has
4. What environment variables and external dependencies it needs

Request the most important files you haven't seen yet.

## Response Format (JSON only, no markdown fences)
{
  "files": ["src/gateway/index.ts", "apps/api/Dockerfile", "scripts/build.sh"],
  "reason": "Need to understand the gateway entry point and API build process",
  "done": false,
  "analysis": ""
}

If you already have enough context from the files shown, set "done": true and provide your analysis:
{
  "files": [],
  "reason": "",
  "done": true,
  "analysis": "This is a pnpm monorepo with a WebSocket gateway on port 18789..."
}`)
	} else {
		b.WriteString(fmt.Sprintf(`
## Round %d — Request More Files or Finish
Based on the files you've read so far, do you need to read more files?
If yes, request them. If no, set done=true and provide your full analysis of how to build and deploy this app.

## Response Format (JSON only, no markdown fences)
{
  "files": ["some/other/file.ts"],
  "reason": "Need to check the worker process entry point",
  "done": false,
  "analysis": ""
}`, round+1))
	}

	return b.String()
}

func parseFileRequest(raw string) (*FileRequest, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var req FileRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, fmt.Errorf("failed to parse file request: %w", err)
	}
	return &req, nil
}

// readRepoFile reads a file from the cloned repo, capped at maxFileSize
func readRepoFile(clonePath, relPath string) string {
	// sanitize — prevent path traversal
	cleanPath := filepath.Clean(relPath)
	if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") {
		return ""
	}

	fp := filepath.Join(clonePath, cleanPath)
	data, err := os.ReadFile(fp)
	if err != nil {
		return ""
	}

	content := string(data)
	if len(content) > maxFileSize {
		content = content[:maxFileSize] + "\n... (truncated)"
	}
	return content
}
