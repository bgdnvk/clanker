package deploy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// RepoProfile is the result of analyzing a git repo
type RepoProfile struct {
	RepoURL        string            `json:"repoUrl"`
	ClonePath      string            `json:"clonePath"`
	Language       string            `json:"language"`       // go, python, node, rust, java, etc
	Framework      string            `json:"framework"`      // express, flask, fastapi, gin, fiber, nextjs, etc
	PackageManager string            `json:"packageManager"` // npm, pnpm, yarn, bun, pip, cargo, go
	IsMonorepo     bool              `json:"isMonorepo"`
	HasDocker      bool              `json:"hasDocker"`
	HasCompose     bool              `json:"hasCompose"`  // docker-compose.yml
	DeployHints    []string          `json:"deployHints"` // fly.toml, render.yaml, etc
	Ports          []int             `json:"ports"`
	EnvVars        []string          `json:"envVars"`    // required env vars detected
	EntryPoint     string            `json:"entryPoint"` // main.go, app.py, index.js, etc
	BuildCmd       string            `json:"buildCmd"`
	StartCmd       string            `json:"startCmd"`
	HasDB          bool              `json:"hasDb"`
	DBType         string            `json:"dbType"` // postgres, mysql, redis, mongo, etc
	Summary        string            `json:"summary"`
	KeyFiles       map[string]string `json:"keyFiles"` // filename → content (capped)
	FileTree       string            `json:"fileTree"` // top-level directory listing
}

// CloneAndAnalyze clones a repo and returns a profile
func CloneAndAnalyze(ctx context.Context, repoURL string) (*RepoProfile, error) {
	tmpDir, err := os.MkdirTemp("", "clanker-deploy-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("git clone failed: %w\n%s", err, string(out))
	}

	profile, err := Analyze(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	profile.RepoURL = repoURL
	profile.ClonePath = tmpDir
	profile.KeyFiles = readKeyFiles(tmpDir)
	profile.FileTree = buildFileTree(tmpDir, "", 0)
	profile.Summary = buildSummary(profile)
	return profile, nil
}

// Analyze inspects a local directory
func Analyze(dir string) (*RepoProfile, error) {
	p := &RepoProfile{}

	// detect Dockerfile and docker-compose
	p.HasDocker = fileExists(dir, "Dockerfile") || fileExists(dir, "dockerfile")
	p.HasCompose = fileExists(dir, "docker-compose.yml") || fileExists(dir, "docker-compose.yaml") || fileExists(dir, "compose.yml") || fileExists(dir, "compose.yaml")

	detectLanguage(dir, p)
	detectPackageManager(dir, p)
	detectMonorepo(dir, p)
	detectDeployHints(dir, p)
	detectPorts(dir, p)
	detectEnvVars(dir, p)
	detectDatabase(dir, p)
	detectCommands(dir, p)

	return p, nil
}

func detectLanguage(dir string, p *RepoProfile) {
	// Go
	if fileExists(dir, "go.mod") {
		p.Language = "go"
		if contentContains(dir, "go.mod", "gin-gonic") {
			p.Framework = "gin"
		} else if contentContains(dir, "go.mod", "gofiber") || contentContains(dir, "go.mod", "fiber") {
			p.Framework = "fiber"
		} else if contentContains(dir, "go.mod", "echo") {
			p.Framework = "echo"
		} else if contentContains(dir, "go.mod", "chi") {
			p.Framework = "chi"
		}
		p.EntryPoint = findGoEntryPoint(dir)
		return
	}

	// Python
	if fileExists(dir, "requirements.txt") || fileExists(dir, "pyproject.toml") || fileExists(dir, "setup.py") {
		p.Language = "python"
		depFile := "requirements.txt"
		if !fileExists(dir, depFile) {
			depFile = "pyproject.toml"
		}
		if contentContains(dir, depFile, "fastapi") {
			p.Framework = "fastapi"
		} else if contentContains(dir, depFile, "flask") {
			p.Framework = "flask"
		} else if contentContains(dir, depFile, "django") {
			p.Framework = "django"
		} else if contentContains(dir, depFile, "streamlit") {
			p.Framework = "streamlit"
		} else if contentContains(dir, depFile, "gradio") {
			p.Framework = "gradio"
		}
		p.EntryPoint = findPythonEntryPoint(dir)
		return
	}

	// Node / JS / TS
	if fileExists(dir, "package.json") {
		p.Language = "node"
		if contentContains(dir, "package.json", "next") {
			p.Framework = "nextjs"
		} else if contentContains(dir, "package.json", "express") {
			p.Framework = "express"
		} else if contentContains(dir, "package.json", "fastify") {
			p.Framework = "fastify"
		} else if contentContains(dir, "package.json", "nuxt") {
			p.Framework = "nuxt"
		} else if contentContains(dir, "package.json", "react") {
			p.Framework = "react"
		} else if contentContains(dir, "package.json", "vite") {
			p.Framework = "vite"
		}
		p.EntryPoint = findNodeEntryPoint(dir)
		return
	}

	// Rust
	if fileExists(dir, "Cargo.toml") {
		p.Language = "rust"
		if contentContains(dir, "Cargo.toml", "actix") {
			p.Framework = "actix"
		} else if contentContains(dir, "Cargo.toml", "axum") {
			p.Framework = "axum"
		} else if contentContains(dir, "Cargo.toml", "rocket") {
			p.Framework = "rocket"
		}
		p.EntryPoint = "src/main.rs"
		return
	}

	// Java
	if fileExists(dir, "pom.xml") || fileExists(dir, "build.gradle") || fileExists(dir, "build.gradle.kts") {
		p.Language = "java"
		if fileExists(dir, "pom.xml") && contentContains(dir, "pom.xml", "spring-boot") {
			p.Framework = "spring-boot"
		}
		return
	}

	p.Language = "unknown"
}

// detectPackageManager figures out npm/pnpm/yarn/bun from lock files
func detectPackageManager(dir string, p *RepoProfile) {
	switch p.Language {
	case "node":
		if fileExists(dir, "pnpm-lock.yaml") || fileExists(dir, "pnpm-workspace.yaml") {
			p.PackageManager = "pnpm"
		} else if fileExists(dir, "yarn.lock") {
			p.PackageManager = "yarn"
		} else if fileExists(dir, "bun.lockb") || fileExists(dir, "bun.lock") {
			p.PackageManager = "bun"
		} else {
			p.PackageManager = "npm"
		}
	case "python":
		if fileExists(dir, "Pipfile") || fileExists(dir, "Pipfile.lock") {
			p.PackageManager = "pipenv"
		} else if fileExists(dir, "poetry.lock") || (fileExists(dir, "pyproject.toml") && contentContains(dir, "pyproject.toml", "[tool.poetry]")) {
			p.PackageManager = "poetry"
		} else if fileExists(dir, "uv.lock") {
			p.PackageManager = "uv"
		} else {
			p.PackageManager = "pip"
		}
	case "go":
		p.PackageManager = "go"
	case "rust":
		p.PackageManager = "cargo"
	case "java":
		if fileExists(dir, "pom.xml") {
			p.PackageManager = "maven"
		} else {
			p.PackageManager = "gradle"
		}
	}
}

// detectMonorepo checks for workspace/monorepo indicators
func detectMonorepo(dir string, p *RepoProfile) {
	// pnpm workspaces
	if fileExists(dir, "pnpm-workspace.yaml") {
		p.IsMonorepo = true
		return
	}
	// npm/yarn workspaces in package.json
	if fileExists(dir, "package.json") && contentContains(dir, "package.json", "workspaces") {
		p.IsMonorepo = true
		return
	}
	// lerna
	if fileExists(dir, "lerna.json") {
		p.IsMonorepo = true
		return
	}
	// turborepo
	if fileExists(dir, "turbo.json") {
		p.IsMonorepo = true
		return
	}
	// nx
	if fileExists(dir, "nx.json") {
		p.IsMonorepo = true
		return
	}
	// go workspace
	if fileExists(dir, "go.work") {
		p.IsMonorepo = true
		return
	}
}

// detectDeployHints checks for existing deploy config files
func detectDeployHints(dir string, p *RepoProfile) {
	hints := map[string]string{
		"fly.toml":                      "fly.io",
		"render.yaml":                   "render",
		"vercel.json":                   "vercel",
		"netlify.toml":                  "netlify",
		"railway.json":                  "railway",
		"Procfile":                      "heroku",
		"app.yaml":                      "gcp-app-engine",
		"terraform/main.tf":             "terraform",
		"serverless.yml":                "serverless",
		"cdk.json":                      "aws-cdk",
		"pulumi.yaml":                   "pulumi",
		"wrangler.toml":                 "cloudflare",
		"wrangler.jsonc":                "cloudflare",
		"wrangler.json":                 "cloudflare",
		".github/workflows/deploy.yml":  "github-actions",
		".github/workflows/deploy.yaml": "github-actions",
	}
	for file, hint := range hints {
		if fileExists(dir, file) {
			p.DeployHints = appendUniqueStr(p.DeployHints, hint)
		}
	}
}

func detectPorts(dir string, p *RepoProfile) {
	portRe := regexp.MustCompile(`(?i)(?:EXPOSE\s+|:(\d{4,5})|\bport\b[^=]*=\s*["']?(\d{4,5})|\.listen\((\d{4,5})|addr.*:(\d{4,5}))`)
	exposeRe := regexp.MustCompile(`(?i)^EXPOSE\s+(\d+)`)
	// matches --port 8080 or --port=8080 or -p 3000
	flagPortRe := regexp.MustCompile(`(?:--port[= ]|(?:^|\s)-p\s+)(\d{4,5})`)

	// check Dockerfile EXPOSE
	if p.HasDocker {
		scanFile(filepath.Join(dir, "Dockerfile"), func(line string) {
			if m := exposeRe.FindStringSubmatch(line); m != nil {
				if port := parsePort(m[1]); port > 0 {
					p.Ports = appendUnique(p.Ports, port)
				}
			}
		})
	}

	// check fly.toml internal_port
	if fileExists(dir, "fly.toml") {
		flyPortRe := regexp.MustCompile(`internal_port\s*=\s*(\d+)`)
		scanFile(filepath.Join(dir, "fly.toml"), func(line string) {
			if m := flyPortRe.FindStringSubmatch(line); m != nil {
				if port := parsePort(m[1]); port > 0 {
					p.Ports = appendUnique(p.Ports, port)
				}
			}
		})
	}

	// check package.json scripts for --port flags
	if fileExists(dir, "package.json") {
		data, _ := os.ReadFile(filepath.Join(dir, "package.json"))
		if data != nil {
			for _, m := range flagPortRe.FindAllStringSubmatch(string(data), -1) {
				if port := parsePort(m[1]); port > 0 {
					p.Ports = appendUnique(p.Ports, port)
				}
			}
		}
	}

	// check docker-compose for port mappings like "18789:18789" or "8080:3000"
	if p.HasCompose {
		composeFiles := []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
		composePortRe := regexp.MustCompile(`["']?(\d{4,5}):(\d{4,5})(?:/(?:tcp|udp))?["']?`)
		composeVarHostPortRe := regexp.MustCompile(`['"][^'"]*?:(\d{4,5})(?:/(?:tcp|udp))?['"]`)
		for _, cf := range composeFiles {
			fp := filepath.Join(dir, cf)
			if _, err := os.Stat(fp); err != nil {
				continue
			}
			scanFile(fp, func(line string) {
				if m := composePortRe.FindStringSubmatch(line); m != nil {
					// container port is the right side
					if port := parsePort(m[2]); port > 0 {
						p.Ports = appendUnique(p.Ports, port)
					}
				}
				if m := composeVarHostPortRe.FindStringSubmatch(line); m != nil {
					if port := parsePort(m[1]); port > 0 {
						p.Ports = appendUnique(p.Ports, port)
					}
				}
			})
		}
	}

	// scan common source files for port patterns
	candidates := []string{"main.go", "app.py", "main.py", "server.py", "index.js", "server.js", "app.js", "src/main.rs", "src/index.ts", "src/server.ts", "src/main.ts", "gateway/src/index.ts"}
	for _, f := range candidates {
		fp := filepath.Join(dir, f)
		if _, err := os.Stat(fp); err != nil {
			continue
		}
		scanFile(fp, func(line string) {
			for _, m := range portRe.FindAllStringSubmatch(line, -1) {
				for _, g := range m[1:] {
					if port := parsePort(g); port > 0 {
						p.Ports = appendUnique(p.Ports, port)
					}
				}
			}
		})
	}

	// default port heuristics
	if len(p.Ports) == 0 {
		switch p.Framework {
		case "nextjs", "nuxt", "vite", "react":
			p.Ports = []int{3000}
		case "fastapi", "flask", "django", "streamlit", "gradio":
			p.Ports = []int{8000}
		case "express", "fastify":
			p.Ports = []int{3000}
		case "gin", "echo", "fiber", "chi":
			p.Ports = []int{8080}
		case "spring-boot":
			p.Ports = []int{8080}
		case "actix", "axum", "rocket":
			p.Ports = []int{8080}
		default:
			p.Ports = []int{8080}
		}
	}
}

func detectEnvVars(dir string, p *RepoProfile) {
	envFiles := []string{".env.example", ".env.sample", ".env.template", ".env"}
	for _, f := range envFiles {
		fp := filepath.Join(dir, f)
		if _, err := os.Stat(fp); err != nil {
			continue
		}
		scanFile(fp, func(line string) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				return
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) >= 1 {
				key := strings.TrimSpace(parts[0])
				if key != "" {
					p.EnvVars = appendUniqueStr(p.EnvVars, key)
				}
			}
		})
	}
}

func detectDatabase(dir string, p *RepoProfile) {
	dbPatterns := map[string][]string{
		"postgres": {"psycopg", "pg", "postgres", "pgx", "sqlalchemy", "prisma", "sequelize", "typeorm", "lib/pq", "jackc/pgx"},
		"mysql":    {"mysql", "pymysql", "mysqlclient", "mysql2"},
		"redis":    {"redis", "ioredis", "go-redis"},
		"mongo":    {"mongo", "pymongo", "mongoose"},
		"sqlite":   {"sqlite", "better-sqlite3", "mattn/go-sqlite3"},
	}

	depFiles := []string{"go.mod", "requirements.txt", "pyproject.toml", "package.json", "Cargo.toml", "pom.xml"}
	for _, f := range depFiles {
		fp := filepath.Join(dir, f)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		for dbType, patterns := range dbPatterns {
			for _, pat := range patterns {
				if strings.Contains(content, strings.ToLower(pat)) {
					p.HasDB = true
					p.DBType = dbType
					return
				}
			}
		}
	}
}

func detectCommands(dir string, p *RepoProfile) {
	switch p.Language {
	case "go":
		p.BuildCmd = "go build -o app ."
		p.StartCmd = "./app"
	case "python":
		switch p.Framework {
		case "fastapi":
			p.BuildCmd = "pip install -r requirements.txt"
			p.StartCmd = "uvicorn main:app --host 0.0.0.0 --port 8000"
		case "flask":
			p.BuildCmd = "pip install -r requirements.txt"
			p.StartCmd = "gunicorn app:app -b 0.0.0.0:8000"
		case "django":
			p.BuildCmd = "pip install -r requirements.txt"
			p.StartCmd = "gunicorn config.wsgi -b 0.0.0.0:8000"
		default:
			p.BuildCmd = "pip install -r requirements.txt"
			p.StartCmd = "python " + p.EntryPoint
		}
		// override with pipenv/poetry if detected
		if p.PackageManager == "pipenv" {
			p.BuildCmd = "pipenv install"
		} else if p.PackageManager == "poetry" {
			p.BuildCmd = "poetry install"
		} else if p.PackageManager == "uv" {
			p.BuildCmd = "uv sync"
		}
	case "node":
		// pick install+build cmd based on package manager
		switch p.PackageManager {
		case "pnpm":
			p.BuildCmd = "pnpm install && pnpm run build"
			p.StartCmd = "pnpm start"
		case "yarn":
			p.BuildCmd = "yarn install --frozen-lockfile && yarn build"
			p.StartCmd = "yarn start"
		case "bun":
			p.BuildCmd = "bun install && bun run build"
			p.StartCmd = "bun start"
		default:
			p.BuildCmd = "npm ci && npm run build"
			p.StartCmd = "npm start"
		}
		// try to read actual start script from package.json
		if fileExists(dir, "package.json") {
			data, _ := os.ReadFile(filepath.Join(dir, "package.json"))
			if data != nil {
				content := string(data)
				// detect custom start scripts
				if strings.Contains(content, `"start:prod"`) {
					p.StartCmd = p.PackageManager + " run start:prod"
				}
			}
		}
	case "rust":
		p.BuildCmd = "cargo build --release"
		p.StartCmd = "./target/release/app"
	case "java":
		if fileExists(dir, "pom.xml") {
			p.BuildCmd = "mvn package -DskipTests"
			p.StartCmd = "java -jar target/*.jar"
		} else {
			p.BuildCmd = "gradle build"
			p.StartCmd = "java -jar build/libs/*.jar"
		}
	}

	// if Dockerfile exists, override with docker build (the Dockerfile knows how to build)
	if p.HasDocker {
		p.BuildCmd = "docker build -t app ."
		if len(p.Ports) > 0 {
			p.StartCmd = fmt.Sprintf("docker run -p %d:%d app", p.Ports[0], p.Ports[0])
		} else {
			p.StartCmd = "docker run app"
		}
	}
}

func buildSummary(p *RepoProfile) string {
	parts := []string{}
	if p.Language != "" && p.Language != "unknown" {
		lang := strings.Title(p.Language)
		if p.Framework != "" {
			parts = append(parts, fmt.Sprintf("%s/%s application", lang, p.Framework))
		} else {
			parts = append(parts, fmt.Sprintf("%s application", lang))
		}
	}
	if p.PackageManager != "" {
		parts = append(parts, "pkg: "+p.PackageManager)
	}
	if p.IsMonorepo {
		parts = append(parts, "monorepo")
	}
	if p.HasDocker {
		parts = append(parts, "has Dockerfile")
	}
	if p.HasCompose {
		parts = append(parts, "has docker-compose")
	}
	if len(p.Ports) > 0 {
		portStrs := []string{}
		for _, port := range p.Ports {
			portStrs = append(portStrs, fmt.Sprintf("%d", port))
		}
		parts = append(parts, "exposes port(s) "+strings.Join(portStrs, ", "))
	}
	if p.HasDB {
		parts = append(parts, "uses "+p.DBType)
	}
	if len(p.EnvVars) > 0 {
		parts = append(parts, fmt.Sprintf("%d env vars required", len(p.EnvVars)))
	}
	if len(p.DeployHints) > 0 {
		parts = append(parts, "deploy hints: "+strings.Join(p.DeployHints, ", "))
	}
	return strings.Join(parts, " • ")
}

// --- key file reader (feeds LLM real context) ---

const maxFileBytes = 4096 // cap per file to avoid blowing up context

// readKeyFiles reads the actual content of important repo files
func readKeyFiles(dir string) map[string]string {
	files := map[string]string{}

	// files we always want if they exist
	important := []string{
		"Dockerfile", "dockerfile",
		"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
		"package.json",
		"go.mod",
		"requirements.txt", "pyproject.toml",
		"Cargo.toml",
		"pom.xml",
		"Makefile",
		"fly.toml",
		"render.yaml",
		"Procfile",
		"vercel.json",
		"netlify.toml",
		"wrangler.toml", "wrangler.jsonc", "wrangler.json",
		".env.example", ".env.sample", ".env.template",
		"README.md", "readme.md", "README",
		"pnpm-workspace.yaml",
		"turbo.json",
		"nx.json",
		"tsconfig.json",
	}

	for _, name := range important {
		fp := filepath.Join(dir, name)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxFileBytes {
			content = content[:maxFileBytes] + "\n... (truncated)"
		}
		files[name] = content
	}
	return files
}

// buildFileTree creates a compact directory listing (2 levels deep)
func buildFileTree(dir, prefix string, depth int) string {
	if depth > 2 {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		// skip noise
		if name == ".git" || name == "node_modules" || name == ".next" || name == "__pycache__" || name == "target" || name == "dist" || name == "build" || name == ".cache" || name == "vendor" {
			continue
		}
		if e.IsDir() {
			b.WriteString(prefix + name + "/\n")
			b.WriteString(buildFileTree(filepath.Join(dir, name), prefix+"  ", depth+1))
		} else {
			b.WriteString(prefix + name + "\n")
		}
	}
	return b.String()
}

// --- helpers ---

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func contentContains(dir, name, substr string) bool {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), strings.ToLower(substr))
}

func scanFile(path string, fn func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fn(s.Text())
	}
}

func parsePort(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var port int
	fmt.Sscanf(s, "%d", &port)
	if port >= 1024 && port <= 65535 {
		return port
	}
	return 0
}

func appendUnique(s []int, v int) []int {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func appendUniqueStr(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func findGoEntryPoint(dir string) string {
	if fileExists(dir, "main.go") {
		return "main.go"
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "cmd"))
	for _, e := range entries {
		if e.IsDir() {
			if fileExists(filepath.Join(dir, "cmd", e.Name()), "main.go") {
				return filepath.Join("cmd", e.Name(), "main.go")
			}
		}
	}
	return "main.go"
}

func findPythonEntryPoint(dir string) string {
	for _, name := range []string{"main.py", "app.py", "server.py", "run.py"} {
		if fileExists(dir, name) {
			return name
		}
	}
	return "main.py"
}

func findNodeEntryPoint(dir string) string {
	for _, name := range []string{"index.js", "server.js", "app.js", "src/index.js", "src/server.js", "src/index.ts", "src/server.ts"} {
		if fileExists(dir, name) {
			return name
		}
	}
	return "index.js"
}
