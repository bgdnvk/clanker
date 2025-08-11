package codebase

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Analyzer struct {
	basePath string
}

type FileInfo struct {
	Path     string
	Language string
	Content  string
}

func NewAnalyzer(basePath string) *Analyzer {
	return &Analyzer{
		basePath: basePath,
	}
}

func (a *Analyzer) GetRelevantContext(question string) (string, error) {
	// Get all relevant files
	files, err := a.scanCodebase()
	if err != nil {
		return "", fmt.Errorf("failed to scan codebase: %w", err)
	}

	// Filter files based on question context
	relevantFiles := a.filterRelevantFiles(files, question)

	// Build context string
	var context strings.Builder
	context.WriteString("Codebase Analysis:\n")

	for _, file := range relevantFiles {
		context.WriteString(fmt.Sprintf("\nFile: %s (%s)\n", file.Path, file.Language))
		context.WriteString("---\n")

		// Include first 50 lines or relevant snippets
		lines := strings.Split(file.Content, "\n")
		maxLines := 50
		if len(lines) < maxLines {
			maxLines = len(lines)
		}

		for i := 0; i < maxLines; i++ {
			context.WriteString(lines[i])
			context.WriteString("\n")
		}

		if len(lines) > maxLines {
			context.WriteString(fmt.Sprintf("... (%d more lines)\n", len(lines)-maxLines))
		}
		context.WriteString("\n")
	}

	return context.String(), nil
}

func (a *Analyzer) scanCodebase() ([]FileInfo, error) {
	var files []FileInfo

	err := filepath.Walk(a.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and hidden files
		if info.IsDir() || strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		// Skip common non-source directories
		if strings.Contains(path, "node_modules") ||
			strings.Contains(path, ".git") ||
			strings.Contains(path, "vendor") ||
			strings.Contains(path, "__pycache__") {
			return nil
		}

		// Only include source code files
		language := detectLanguage(path)
		if language == "" {
			return nil
		}

		content, err := readFileContent(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		files = append(files, FileInfo{
			Path:     path,
			Language: language,
			Content:  content,
		})

		return nil
	})

	return files, err
}

func (a *Analyzer) filterRelevantFiles(files []FileInfo, question string) []FileInfo {
	questionLower := strings.ToLower(question)
	var relevant []FileInfo

	// Keywords that might indicate relevant files
	keywords := extractKeywords(questionLower)

	for _, file := range files {
		score := 0
		fileContentLower := strings.ToLower(file.Content)
		filePathLower := strings.ToLower(file.Path)

		// Check if question keywords appear in file path or content
		for _, keyword := range keywords {
			if strings.Contains(filePathLower, keyword) {
				score += 3
			}
			if strings.Contains(fileContentLower, keyword) {
				score += 1
			}
		}

		// Include files with score > 0, limit to top 10 files
		if score > 0 {
			relevant = append(relevant, file)
		}

		if len(relevant) >= 10 {
			break
		}
	}

	return relevant
}

func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js", ".jsx":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".java":
		return "Java"
	case ".cpp", ".cc", ".cxx":
		return "C++"
	case ".c":
		return "C"
	case ".rs":
		return "Rust"
	case ".rb":
		return "Ruby"
	case ".php":
		return "PHP"
	case ".cs":
		return "C#"
	case ".swift":
		return "Swift"
	case ".kt":
		return "Kotlin"
	case ".scala":
		return "Scala"
	case ".sh":
		return "Shell"
	case ".sql":
		return "SQL"
	case ".yaml", ".yml":
		return "YAML"
	case ".json":
		return "JSON"
	case ".xml":
		return "XML"
	case ".tf":
		return "Terraform"
	case ".dockerfile":
		return "Dockerfile"
	default:
		if strings.HasSuffix(path, "Dockerfile") {
			return "Dockerfile"
		}
		if strings.HasSuffix(path, "Makefile") {
			return "Makefile"
		}
		return ""
	}
}

func readFileContent(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var content strings.Builder
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < 1000 { // Limit to first 1000 lines
		content.WriteString(scanner.Text())
		content.WriteString("\n")
		lineCount++
	}

	return content.String(), scanner.Err()
}

func extractKeywords(question string) []string {
	// Simple keyword extraction - could be enhanced with NLP
	words := strings.Fields(question)
	var keywords []string

	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
		"is": true, "are": true, "was": true, "were": true, "what": true, "where": true,
		"when": true, "how": true, "why": true, "who": true, "which": true,
	}

	for _, word := range words {
		word = strings.ToLower(strings.Trim(word, ".,!?;:"))
		if len(word) > 2 && !stopWords[word] {
			keywords = append(keywords, word)
		}
	}

	return keywords
}
