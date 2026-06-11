package codeview

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxFilesScanned        = 5000
	maxFileBytes           = 512 * 1024
	maxEvidencePerFile     = 2
	maxEvidencePerPattern  = 8
	maxPatternFiles        = 80
	maxGraphFileNodes      = 140
	maxImportEdges         = 180
	maxCorrelations        = 220
	maxCorrelationFiles    = 20
	maxCorrelationEvidence = 4
	maxCorrelationNodes    = 80
)

type AnalyzeOptions struct {
	KeepClone bool
}

type Analysis struct {
	RepoURL            string         `json:"repoUrl"`
	ClonePath          string         `json:"clonePath,omitempty"`
	GeneratedAt        time.Time      `json:"generatedAt"`
	Summary            Summary        `json:"summary"`
	SupportedLanguages []LanguageSpec `json:"supportedLanguages"`
	Languages          []LanguageStat `json:"languages"`
	Files              []CodeFile     `json:"files"`
	Patterns           []CodePattern  `json:"patterns"`
	Correlations       []Correlation  `json:"correlations"`
	Graph              CodeGraph      `json:"graph"`
	Subagents          []SubagentRun  `json:"subagents"`
	Warnings           []string       `json:"warnings,omitempty"`
}

type Summary struct {
	PrimaryLanguage  string `json:"primaryLanguage"`
	TotalFiles       int    `json:"totalFiles"`
	SourceFiles      int    `json:"sourceFiles"`
	TotalLines       int    `json:"totalLines"`
	PatternCount     int    `json:"patternCount"`
	CorrelationCount int    `json:"correlationCount"`
	ConnectionCount  int    `json:"connectionCount"`
	EntryPoint       string `json:"entryPoint,omitempty"`
	Framework        string `json:"framework,omitempty"`
	HasAuth          bool   `json:"hasAuth"`
	HasDatabase      bool   `json:"hasDatabase"`
	HasMiddleware    bool   `json:"hasMiddleware"`
	HasTests         bool   `json:"hasTests"`
}

type LanguageSpec struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Extensions []string `json:"extensions"`
}

type LanguageStat struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Files      int      `json:"files"`
	Lines      int      `json:"lines"`
	Percentage float64  `json:"percentage"`
	Extensions []string `json:"extensions"`
}

type CodeFile struct {
	Path     string   `json:"path"`
	Language string   `json:"language"`
	Lines    int      `json:"lines"`
	Bytes    int64    `json:"bytes"`
	Patterns []string `json:"patterns,omitempty"`
	Imports  []string `json:"imports,omitempty"`
}

type CodePattern struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
	Confidence  float64    `json:"confidence"`
	Files       []string   `json:"files"`
	Evidence    []Evidence `json:"evidence"`
}

type Evidence struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
	Reason  string `json:"reason"`
}

type Correlation struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Label    string     `json:"label"`
	Value    string     `json:"value"`
	Source   string     `json:"source"`
	Files    []string   `json:"files"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type CodeGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Label    string                 `json:"label"`
	Group    string                 `json:"group,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type GraphEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
	Label  string `json:"label,omitempty"`
}

type SubagentRun struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Findings []string `json:"findings,omitempty"`
	Duration string   `json:"duration"`
}

type languageDef struct {
	id         string
	label      string
	extensions []string
}

type patternDef struct {
	id          string
	label       string
	category    string
	description string
	pathHints   []string
	nameHints   []string
	tokens      []string
}

type scannedFile struct {
	path     string
	language string
	lines    int
	bytes    int64
	content  string
	imports  []string
	patterns map[string][]Evidence
}

var languageDefs = []languageDef{
	{id: "javascript", label: "JavaScript", extensions: []string{".js", ".jsx", ".mjs", ".cjs"}},
	{id: "typescript", label: "TypeScript", extensions: []string{".ts", ".tsx", ".mts", ".cts"}},
	{id: "python", label: "Python", extensions: []string{".py"}},
	{id: "java", label: "Java", extensions: []string{".java"}},
	{id: "go", label: "Go", extensions: []string{".go"}},
	{id: "rust", label: "Rust", extensions: []string{".rs"}},
	{id: "csharp", label: "C#", extensions: []string{".cs"}},
	{id: "cpp", label: "C++", extensions: []string{".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"}},
	{id: "c", label: "C", extensions: []string{".c", ".h"}},
	{id: "php", label: "PHP", extensions: []string{".php"}},
	{id: "ruby", label: "Ruby", extensions: []string{".rb"}},
	{id: "sql", label: "SQL", extensions: []string{".sql"}},
	{id: "terraform", label: "Terraform", extensions: []string{".tf", ".tfvars"}},
	{id: "yaml", label: "YAML", extensions: []string{".yml", ".yaml"}},
	{id: "shell", label: "Shell", extensions: []string{".sh", ".bash", ".zsh"}},
	{id: "markdown", label: "Markdown", extensions: []string{".md", ".mdx"}},
}

var languageByExtension = buildLanguageIndex()

var patternDefs = []patternDef{
	{
		id: "entry_point", label: "Entry Point", category: "Inputs",
		description: "Application bootstrap, CLI startup, or server startup code.",
		pathHints:   []string{"cmd/", "src/main", "main.", "server.", "app.", "index."},
		nameHints:   []string{"main.go", "main.ts", "main.js", "main.py", "server.ts", "server.js", "app.py", "index.ts", "index.js"},
		tokens:      []string{"func main(", "if __name__ == \"__main__\"", "app.listen(", "server.listen(", "SpringApplication.run", "public static void main", "tokio::main", "asyncio.run("},
	},
	{
		id: "config", label: "Config", category: "Cross-Cutting",
		description: "Runtime configuration, environment variables, and settings loading.",
		pathHints:   []string{"config/", "settings/", "env/", ".env"},
		nameHints:   []string{"config.ts", "config.js", "settings.py", "env.ts", ".env.example", "application.yml", "application.properties"},
		tokens:      []string{"process.env", "os.Getenv", "viper.Get", "dotenv", "BaseSettings", "env::var", "System.getenv", "ConfigurationBuilder", "config("},
	},
	{
		id: "routes_handlers", label: "Routes / Handlers", category: "Inputs",
		description: "HTTP routes, controllers, request handlers, webhooks, or CLI command handlers.",
		pathHints:   []string{"routes/", "controllers/", "handlers/", "api/", "webhooks/"},
		nameHints:   []string{"routes.ts", "routes.js", "controller.ts", "controller.java", "handlers.go", "routes.go"},
		tokens:      []string{"express.Router", "router.get", "router.post", "app.get(", "app.post(", "@app.route", "@router.", "gin.Context", ".GET(", ".POST(", "@GetMapping", "@PostMapping", "http.Handler"},
	},
	{
		id: "services", label: "Services / Business Logic", category: "Logic",
		description: "Product workflows and domain operations between handlers and state.",
		pathHints:   []string{"services/", "service/", "usecases/", "use_cases/", "domain/"},
		nameHints:   []string{"service.ts", "service.js", "service.py", "service.go", "service.java"},
		tokens:      []string{"class ", "Service", "usecase", "UseCase", "business", "workflow"},
	},
	{
		id: "database", label: "Database / Repository", category: "State",
		description: "Database clients, repositories, DAOs, ORM access, and direct SQL.",
		pathHints:   []string{"db/", "database/", "repositories/", "repository/", "dao/", "prisma/", "drizzle/", "sql/"},
		nameHints:   []string{"db.ts", "db.go", "database.py", "repository.ts", "repository.go", "schema.prisma"},
		tokens:      []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "CREATE TABLE", "pgx", "sqlx", "gorm", "prisma", "sequelize", "typeorm", "mongoose", "sqlalchemy", "psycopg", "mysql", "redis"},
	},
	{
		id: "models", label: "Models / Entities / Types", category: "State",
		description: "Domain models, entities, DTOs, structs, interfaces, or persistent types.",
		pathHints:   []string{"models/", "entities/", "entity/", "types/", "dto/", "schemas/"},
		nameHints:   []string{"model.ts", "model.go", "entity.java", "types.ts", "dto.ts"},
		tokens:      []string{"interface ", "type ", "struct ", "class ", "@Entity", "dataclass", "pydantic", "record "},
	},
	{
		id: "auth", label: "Auth", category: "Cross-Cutting",
		description: "Authentication, authorization, sessions, OAuth, JWT, password, or permission logic.",
		pathHints:   []string{"auth/", "oauth/", "session/", "permissions/", "rbac/"},
		nameHints:   []string{"auth.ts", "auth.go", "jwt.ts", "session.py", "permissions.ts"},
		tokens:      []string{"jwt", "JWT", "oauth", "OAuth", "Bearer", "Authorization", "bcrypt", "passport", "session", "permission", "roles", "RBAC"},
	},
	{
		id: "middleware", label: "Middleware", category: "Cross-Cutting",
		description: "Request middleware, interceptors, filters, guards, or cross-cutting request hooks.",
		pathHints:   []string{"middleware/", "middlewares/", "interceptors/", "filters/", "guards/"},
		nameHints:   []string{"middleware.ts", "middleware.go", "interceptor.java", "guard.ts"},
		tokens:      []string{"middleware", "next()", "NextFunction", "Use(", "app.use(", "@Middleware", "intercept(", "Filter", "Guard"},
	},
	{
		id: "validation", label: "Validation / Schema", category: "Cross-Cutting",
		description: "Input validation, schema parsing, DTO validation, and request constraints.",
		pathHints:   []string{"validation/", "validators/", "schemas/", "schema/", "dto/"},
		nameHints:   []string{"schema.ts", "validator.ts", "validation.py", "dto.java"},
		tokens:      []string{"zod", "joi", "yup", "validate", "validator", "BaseModel", "@Valid", "constraints", "schema.parse"},
	},
	{
		id: "errors", label: "Error Handling", category: "Cross-Cutting",
		description: "Shared errors, exceptions, recovery, and response error contracts.",
		pathHints:   []string{"errors/", "exceptions/", "error/"},
		nameHints:   []string{"error.ts", "errors.go", "exception.java"},
		tokens:      []string{"throw new", "Exception", "Error", "recover(", "panic(", "try {", "catch ", "HTTPException"},
	},
	{
		id: "integrations", label: "External Integrations / Clients", category: "Side Effects",
		description: "Third-party clients, SDK wrappers, API integrations, and outbound HTTP calls.",
		pathHints:   []string{"integrations/", "clients/", "providers/", "external/", "adapters/"},
		nameHints:   []string{"client.ts", "client.go", "adapter.ts", "provider.ts"},
		tokens:      []string{"axios", "fetch(", "http.Client", "requests.", "urllib", "OkHttp", "RestTemplate", "SDK", "apiKey", "webhook"},
	},
	{
		id: "jobs_workers", label: "Jobs / Workers", category: "Side Effects",
		description: "Background jobs, queues, cron tasks, workers, and async processors.",
		pathHints:   []string{"jobs/", "workers/", "worker/", "queues/", "tasks/", "cron/"},
		nameHints:   []string{"worker.ts", "job.go", "tasks.py", "cron.ts"},
		tokens:      []string{"cron", "queue", "worker", "Bull", "Celery", "Sidekiq", "enqueue", "schedule", "background"},
	},
	{
		id: "events", label: "Events", category: "Side Effects",
		description: "Event buses, pub/sub, Kafka, domain events, and message publishing.",
		pathHints:   []string{"events/", "event/", "pubsub/", "kafka/", "messages/"},
		nameHints:   []string{"event.ts", "events.go", "publisher.ts", "subscriber.ts"},
		tokens:      []string{"EventEmitter", "publish", "subscribe", "pubsub", "kafka", "nats", "rabbitmq", "domain event"},
	},
	{
		id: "logging", label: "Logging / Observability", category: "Operations",
		description: "Logging, metrics, tracing, telemetry, and observability adapters.",
		pathHints:   []string{"logging/", "logger/", "observability/", "metrics/", "tracing/", "telemetry/"},
		nameHints:   []string{"logger.ts", "logger.go", "metrics.go", "tracing.ts"},
		tokens:      []string{"logger", "log.", "slog", "zap", "prometheus", "metrics", "OpenTelemetry", "otel", "trace"},
	},
	{
		id: "cache", label: "Cache", category: "State",
		description: "Cache clients, cached reads, Redis, Memcached, and expiry logic.",
		pathHints:   []string{"cache/", "redis/", "memcache/"},
		nameHints:   []string{"cache.ts", "redis.go", "cache.py"},
		tokens:      []string{"cache", "redis", "memcached", "ttl", "expire", "setex"},
	},
	{
		id: "storage", label: "Storage / Files", category: "State",
		description: "File uploads, object storage, blob storage, and durable file providers.",
		pathHints:   []string{"storage/", "uploads/", "files/", "blob/"},
		nameHints:   []string{"storage.ts", "files.go", "upload.py"},
		tokens:      []string{"multer", "S3", "PutObject", "GetObject", "Blob", "upload", "download", "filesystem"},
	},
	{
		id: "notifications", label: "Notifications", category: "Side Effects",
		description: "Email, SMS, push, and notification dispatch flows.",
		pathHints:   []string{"notifications/", "notification/", "email/", "mail/", "sms/"},
		nameHints:   []string{"email.ts", "notification.go", "mailer.py"},
		tokens:      []string{"sendEmail", "nodemailer", "mailgun", "ses", "twilio", "notification", "send_sms"},
	},
	{
		id: "billing", label: "Billing / Payments", category: "Side Effects",
		description: "Payments, subscriptions, invoices, checkout, and billing gates.",
		pathHints:   []string{"billing/", "payments/", "payment/", "subscriptions/", "invoices/"},
		nameHints:   []string{"billing.ts", "payments.go", "stripe.py"},
		tokens:      []string{"stripe", "checkout", "invoice", "subscription", "payment", "billing", "entitlement"},
	},
	{
		id: "feature_flags", label: "Feature Flags / Entitlements", category: "Cross-Cutting",
		description: "Feature gates, rollout flags, plans, entitlements, and access tiers.",
		pathHints:   []string{"flags/", "feature-flags/", "entitlements/", "plans/"},
		nameHints:   []string{"featureFlags.ts", "entitlements.go", "plans.py"},
		tokens:      []string{"feature flag", "featureFlag", "launchdarkly", "entitlement", "plan", "tier", "isEnabled"},
	},
	{
		id: "tests", label: "Tests", category: "Operations",
		description: "Unit, integration, e2e, and regression tests.",
		pathHints:   []string{"tests/", "__tests__/", "test/", "spec/"},
		nameHints:   []string{"_test.go", ".test.ts", ".test.js", ".spec.ts", ".spec.js", "test.py"},
		tokens:      []string{"describe(", "it(", "expect(", "assert", "t.Run(", "pytest", "unittest", "@Test"},
	},
	{
		id: "migrations", label: "Migrations / Seeds", category: "Operations",
		description: "Database migrations, seeds, and schema evolution files.",
		pathHints:   []string{"migrations/", "migration/", "seeds/", "seed/"},
		nameHints:   []string{"alembic.ini", "schema.prisma"},
		tokens:      []string{"CREATE TABLE", "ALTER TABLE", "DROP TABLE", "migration", "seed"},
	},
	{
		id: "scripts", label: "Scripts / Tools", category: "Operations",
		description: "Developer scripts, operational tools, setup, seed, and maintenance utilities.",
		pathHints:   []string{"scripts/", "tools/", "bin/", "hack/"},
		nameHints:   []string{"Makefile", "justfile", "setup.sh", "bootstrap.sh"},
		tokens:      []string{"#!/", "make ", "go run", "npm run", "pnpm", "yarn"},
	},
	{
		id: "infrastructure", label: "Infrastructure / Deployment", category: "Operations",
		description: "Deployment descriptors, infrastructure-as-code, containers, CI/CD, and runtime manifests.",
		pathHints:   []string{"infra/", "terraform/", "k8s/", "kubernetes/", ".github/workflows/", "deploy/", "charts/"},
		nameHints:   []string{"Dockerfile", "docker-compose.yml", "compose.yaml", "main.tf", "vercel.json", "netlify.toml", "wrangler.toml"},
		tokens:      []string{"terraform", "resource \"", "apiVersion:", "kind:", "FROM ", "docker compose", "serverless", "pulumi"},
	},
	{
		id: "documentation", label: "Documentation", category: "Operations",
		description: "README, docs, API docs, architecture notes, and runbooks.",
		pathHints:   []string{"docs/", "documentation/", "runbooks/"},
		nameHints:   []string{"README.md", "ARCHITECTURE.md", "API.md", "CHANGELOG.md"},
		tokens:      []string{"# ", "## ", "architecture", "runbook", "getting started"},
	},
	{
		id: "utils", label: "Utils / Helpers", category: "Cross-Cutting",
		description: "Shared helpers, common utilities, formatting, retries, IDs, and date helpers.",
		pathHints:   []string{"utils/", "helpers/", "common/", "shared/", "lib/"},
		nameHints:   []string{"utils.ts", "helpers.go", "common.py", "retry.ts"},
		tokens:      []string{"helper", "util", "retry", "uuid", "format", "normalize"},
	},
}

var importantPatternOrder = []string{
	"entry_point", "routes_handlers", "services", "database", "auth", "middleware", "validation",
	"config", "integrations", "jobs_workers", "events", "logging", "cache", "storage", "billing",
	"feature_flags", "tests", "migrations", "infrastructure", "documentation", "utils",
}

var relativeImportRE = regexp.MustCompile(`(?m)(?:import\s+(?:[^'"]+\s+from\s+)?|require\()\s*['"](\.{1,2}/[^'"]+)['"]`)
var pythonImportRE = regexp.MustCompile(`(?m)^\s*from\s+(\.{1,2}[A-Za-z0-9_./]*)\s+import\s+`)
var issueKeyRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
var otelServiceNameRE = regexp.MustCompile(`(?i)(?:OTEL_SERVICE_NAME|service[._-]?name)\s*[:=]\s*["']?([A-Za-z0-9._/-]+)`)
var terraformResourceRE = regexp.MustCompile(`^\s*resource\s+"([^"]+)"\s+"([^"]+)"`)
var goModRequireRE = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+/[^\s]+)\s+v[0-9][^\s]*`)
var requirementRE = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*(?:==|>=|<=|~=|>|<|=).*$`)
var cargoDependencyRE = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*=\s*`)

func CloneAndAnalyze(ctx context.Context, repoURL string, opts AnalyzeOptions) (*Analysis, func(), error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, nil, fmt.Errorf("repo URL is required")
	}

	if st, err := os.Stat(repoURL); err == nil && st.IsDir() {
		analysis, err := Analyze(repoURL, repoURL)
		return analysis, func() {}, err
	}

	tmpDir, err := os.MkdirTemp("", "clanker-code-view-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() {
		if !opts.KeepClone {
			_ = os.RemoveAll(tmpDir)
		}
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("git clone failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	analysis, err := Analyze(tmpDir, repoURL)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if opts.KeepClone {
		analysis.ClonePath = tmpDir
	}
	return analysis, cleanup, nil
}

func Analyze(dir string, repoURL string) (*Analysis, error) {
	files, warnings, err := scanRepository(dir)
	if err != nil {
		return nil, err
	}

	languageStarted := time.Now()
	languages := buildLanguageStats(files)
	languageFindings := languageFindings(languages)
	languageDuration := time.Since(languageStarted).Round(time.Millisecond).String()

	patternStarted := time.Now()
	patterns := buildPatterns(files)
	patternDuration := time.Since(patternStarted).Round(time.Millisecond).String()

	correlationStarted := time.Now()
	correlations := buildCorrelations(files)
	correlationDuration := time.Since(correlationStarted).Round(time.Millisecond).String()

	graphStarted := time.Now()
	graph := buildGraph(repoURL, files, languages, patterns, correlations)
	graphDuration := time.Since(graphStarted).Round(time.Millisecond).String()

	summaryStarted := time.Now()
	summary := buildSummary(files, languages, patterns, correlations, graph)
	subagents := []SubagentRun{
		{ID: "language-profiler", Label: "Language Profiler", Status: "done", Summary: fmt.Sprintf("%d source files across %d languages", summary.SourceFiles, len(languages)), Findings: languageFindings, Duration: languageDuration},
		{ID: "pattern-cartographer", Label: "Pattern Cartographer", Status: "done", Summary: fmt.Sprintf("%d codebase patterns mapped", len(patterns)), Findings: topPatternFindings(patterns), Duration: patternDuration},
		{ID: "workspace-correlator", Label: "Workspace Correlator", Status: "done", Summary: fmt.Sprintf("%d workspace correlation hints found", len(correlations)), Findings: correlationFindings(correlations), Duration: correlationDuration},
		{ID: "dependency-mapper", Label: "Dependency Mapper", Status: "done", Summary: fmt.Sprintf("%d graph connections generated", len(graph.Edges)), Findings: dependencyFindings(graph), Duration: graphDuration},
		{ID: "surface-reviewer", Label: "Auth / DB / Middleware Reviewer", Status: "done", Summary: surfaceSummary(summary), Findings: surfaceFindings(patterns), Duration: time.Since(summaryStarted).Round(time.Millisecond).String()},
	}

	analysisFiles := make([]CodeFile, 0, len(files))
	for _, file := range files {
		patternIDs := make([]string, 0, len(file.patterns))
		for id := range file.patterns {
			patternIDs = append(patternIDs, id)
		}
		sort.Strings(patternIDs)
		analysisFiles = append(analysisFiles, CodeFile{
			Path:     file.path,
			Language: file.language,
			Lines:    file.lines,
			Bytes:    file.bytes,
			Patterns: patternIDs,
			Imports:  file.imports,
		})
	}
	sort.SliceStable(analysisFiles, func(i, j int) bool {
		if len(analysisFiles[i].Patterns) != len(analysisFiles[j].Patterns) {
			return len(analysisFiles[i].Patterns) > len(analysisFiles[j].Patterns)
		}
		return analysisFiles[i].Path < analysisFiles[j].Path
	})

	return &Analysis{
		RepoURL:            strings.TrimSpace(repoURL),
		GeneratedAt:        time.Now().UTC(),
		Summary:            summary,
		SupportedLanguages: supportedLanguageSpecs(),
		Languages:          languages,
		Files:              analysisFiles,
		Patterns:           patterns,
		Correlations:       correlations,
		Graph:              graph,
		Subagents:          subagents,
		Warnings:           warnings,
	}, nil
}

func scanRepository(root string) ([]scannedFile, []string, error) {
	files := make([]scannedFile, 0)
	warnings := make([]string, 0)
	totalVisited := 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped %s: %v", relPath(root, path), err))
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipFile(name) {
			return nil
		}
		totalVisited++
		if len(files) >= maxFilesScanned {
			return nil
		}
		rel := relPath(root, path)
		lang := languageForPath(rel)
		if lang == "" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			warnings = append(warnings, fmt.Sprintf("skipped large file %s (%d bytes)", rel, info.Size()))
			return nil
		}
		content, readErr := readTextFile(path)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("skipped binary or unreadable file %s", rel))
			return nil
		}
		sf := scannedFile{
			path:     rel,
			language: lang,
			lines:    countLines(content),
			bytes:    info.Size(),
			content:  content,
			imports:  detectImports(rel, content),
			patterns: make(map[string][]Evidence),
		}
		detectPatterns(&sf)
		files = append(files, sf)
		return nil
	})
	if err != nil {
		return nil, warnings, err
	}
	if totalVisited > maxFilesScanned {
		warnings = append(warnings, fmt.Sprintf("scan capped at %d source files", maxFilesScanned))
	}
	return files, warnings, nil
}

func detectPatterns(file *scannedFile) {
	lowerPath := strings.ToLower(file.path)
	base := strings.ToLower(filepath.Base(file.path))
	lowerContent := strings.ToLower(file.content)

	for _, def := range patternDefs {
		score := 0
		reasons := make([]string, 0, 3)
		for _, hint := range def.pathHints {
			if strings.Contains(lowerPath, strings.ToLower(hint)) {
				score += 2
				reasons = append(reasons, "path matches "+hint)
				break
			}
		}
		for _, hint := range def.nameHints {
			if base == strings.ToLower(hint) || strings.Contains(lowerPath, strings.ToLower(hint)) {
				score += 3
				reasons = append(reasons, "file name matches "+hint)
				break
			}
		}
		tokenEvidence := tokenEvidence(file, def.tokens)
		if len(tokenEvidence) > 0 {
			score += len(tokenEvidence)
			if score > 0 {
				for _, ev := range tokenEvidence {
					file.patterns[def.id] = appendLimitedEvidence(file.patterns[def.id], ev, maxEvidencePerFile)
				}
			}
		}
		if score > 0 && len(file.patterns[def.id]) == 0 {
			line := firstNonEmptyLine(file.content)
			if line == "" {
				line = filepath.Base(file.path)
			}
			reason := "path or file name match"
			if len(reasons) > 0 {
				reason = strings.Join(reasons, ", ")
			}
			file.patterns[def.id] = []Evidence{{
				File:    file.path,
				Line:    1,
				Snippet: trimSnippet(line),
				Reason:  reason,
			}}
		}
	}

	if strings.HasSuffix(lowerPath, ".sql") && strings.Contains(lowerContent, "create table") {
		file.patterns["migrations"] = appendLimitedEvidence(file.patterns["migrations"], Evidence{File: file.path, Line: 1, Snippet: "SQL schema or migration file", Reason: "SQL DDL detected"}, maxEvidencePerFile)
	}
}

func tokenEvidence(file *scannedFile, tokens []string) []Evidence {
	if len(tokens) == 0 || strings.TrimSpace(file.content) == "" {
		return nil
	}
	lowerTokens := make([]string, 0, len(tokens))
	for _, token := range tokens {
		lowerTokens = append(lowerTokens, strings.ToLower(token))
	}

	evidence := make([]Evidence, 0, maxEvidencePerFile)
	scanner := bufio.NewScanner(strings.NewReader(file.content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		lower := strings.ToLower(line)
		for i, token := range lowerTokens {
			if strings.Contains(lower, token) {
				evidence = append(evidence, Evidence{
					File:    file.path,
					Line:    lineNo,
					Snippet: trimSnippet(line),
					Reason:  "contains " + tokens[i],
				})
				if len(evidence) >= maxEvidencePerFile {
					return evidence
				}
				break
			}
		}
	}
	return evidence
}

func buildPatterns(files []scannedFile) []CodePattern {
	byID := make(map[string]*CodePattern)
	fileSet := make(map[string]map[string]bool)
	for _, def := range patternDefs {
		byID[def.id] = &CodePattern{
			ID:          def.id,
			Label:       def.label,
			Category:    def.category,
			Description: def.description,
			Files:       []string{},
			Evidence:    []Evidence{},
		}
		fileSet[def.id] = map[string]bool{}
	}

	for _, file := range files {
		for id, evidence := range file.patterns {
			p := byID[id]
			if p == nil {
				continue
			}
			if !fileSet[id][file.path] && len(p.Files) < maxPatternFiles {
				p.Files = append(p.Files, file.path)
				fileSet[id][file.path] = true
			}
			for _, ev := range evidence {
				if len(p.Evidence) >= maxEvidencePerPattern {
					break
				}
				p.Evidence = append(p.Evidence, ev)
			}
		}
	}

	out := make([]CodePattern, 0, len(byID))
	for _, def := range patternDefs {
		p := byID[def.id]
		if p == nil || len(p.Files) == 0 {
			continue
		}
		p.Confidence = confidenceForPattern(len(p.Files), len(p.Evidence))
		sort.Strings(p.Files)
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi := patternOrder(out[i].ID)
		oj := patternOrder(out[j].ID)
		if oi != oj {
			return oi < oj
		}
		return len(out[i].Files) > len(out[j].Files)
	})
	return out
}

type correlationAccumulator struct {
	item    Correlation
	fileSet map[string]bool
}

type addCorrelationFunc func(correlationType, label, value, source string, ev Evidence)

func buildCorrelations(files []scannedFile) []Correlation {
	byKey := make(map[string]*correlationAccumulator)

	add := func(correlationType, label, value, source string, ev Evidence) {
		correlationType = strings.TrimSpace(correlationType)
		label = strings.TrimSpace(label)
		value = strings.TrimSpace(value)
		source = strings.TrimSpace(source)
		if correlationType == "" || label == "" || value == "" || source == "" {
			return
		}
		key := strings.ToLower(correlationType + ":" + source + ":" + value)
		acc := byKey[key]
		if acc == nil {
			acc = &correlationAccumulator{
				item: Correlation{
					ID:     "correlation:" + stableID(key),
					Type:   correlationType,
					Label:  label,
					Value:  value,
					Source: source,
					Files:  []string{},
				},
				fileSet: map[string]bool{},
			}
			byKey[key] = acc
		}
		if ev.File != "" && !acc.fileSet[ev.File] && len(acc.item.Files) < maxCorrelationFiles {
			acc.item.Files = append(acc.item.Files, ev.File)
			acc.fileSet[ev.File] = true
		}
		acc.item.Evidence = appendLimitedEvidence(acc.item.Evidence, ev, maxCorrelationEvidence)
	}

	for _, file := range files {
		detectManifestCorrelations(file, add)
		detectLineCorrelations(file, add)
	}

	out := make([]Correlation, 0, len(byKey))
	for _, acc := range byKey {
		sort.Strings(acc.item.Files)
		out = append(out, acc.item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi := correlationOrder(out[i].Type)
		oj := correlationOrder(out[j].Type)
		if oi != oj {
			return oi < oj
		}
		if len(out[i].Files) != len(out[j].Files) {
			return len(out[i].Files) > len(out[j].Files)
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > maxCorrelations {
		return out[:maxCorrelations]
	}
	return out
}

func detectManifestCorrelations(file scannedFile, add addCorrelationFunc) {
	base := filepath.Base(file.path)
	switch base {
	case "package.json":
		detectPackageJSONCorrelations(file, add)
	case "go.mod":
		detectGoModCorrelations(file, add)
	case "requirements.txt":
		detectRequirementsCorrelations(file, add)
	case "Cargo.toml":
		detectCargoCorrelations(file, add)
	case "Dockerfile":
		add("deployment", "Container build", "Dockerfile", "dockerfile", Evidence{File: file.path, Line: 1, Snippet: base, Reason: "container build descriptor"})
	}
	if strings.HasPrefix(file.path, ".github/workflows/") {
		add("deployment", filepath.Base(file.path), file.path, "github-actions", Evidence{File: file.path, Line: 1, Snippet: file.path, Reason: "GitHub Actions workflow"})
	}
}

func detectPackageJSONCorrelations(file scannedFile, add addCorrelationFunc) {
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal([]byte(file.content), &pkg); err != nil {
		return
	}
	groups := []struct {
		name string
		deps map[string]string
	}{
		{"dependency", pkg.Dependencies},
		{"devDependency", pkg.DevDependencies},
		{"peerDependency", pkg.PeerDependencies},
		{"optionalDependency", pkg.OptionalDependencies},
	}
	for _, group := range groups {
		for name, version := range group.deps {
			add("dependency", name, name, "package.json", Evidence{File: file.path, Line: 1, Snippet: name + " " + version, Reason: group.name})
		}
	}
}

func detectGoModCorrelations(file scannedFile, add addCorrelationFunc) {
	scanLines(file, func(lineNo int, line string) {
		match := goModRequireRE.FindStringSubmatch(line)
		if len(match) < 2 {
			return
		}
		add("dependency", match[1], match[1], "go.mod", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Go module dependency"})
	})
}

func detectRequirementsCorrelations(file scannedFile, add addCorrelationFunc) {
	scanLines(file, func(lineNo int, line string) {
		match := requirementRE.FindStringSubmatch(line)
		if len(match) < 2 {
			return
		}
		add("dependency", match[1], match[1], "requirements.txt", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Python dependency"})
	})
}

func detectCargoCorrelations(file scannedFile, add addCorrelationFunc) {
	inDependencies := false
	scanLines(file, func(lineNo int, line string) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inDependencies = trimmed == "[dependencies]" || trimmed == "[dev-dependencies]" || strings.HasPrefix(trimmed, "[target.") && strings.Contains(trimmed, ".dependencies]")
			return
		}
		if !inDependencies || strings.HasPrefix(trimmed, "#") {
			return
		}
		match := cargoDependencyRE.FindStringSubmatch(trimmed)
		if len(match) < 2 {
			return
		}
		add("dependency", match[1], match[1], "Cargo.toml", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Rust crate dependency"})
	})
}

func detectLineCorrelations(file scannedFile, add addCorrelationFunc) {
	scanLines(file, func(lineNo int, line string) {
		trimmed := strings.TrimSpace(line)
		for _, match := range issueKeyRE.FindAllString(trimmed, -1) {
			add("work_item", match, match, "issue-key", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Jira/Linear-style issue key"})
		}
		if match := otelServiceNameRE.FindStringSubmatch(trimmed); len(match) > 1 {
			service := strings.Trim(match[1], `"' ,`)
			add("service", service, service, "opentelemetry", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "service name convention"})
		}
		if match := terraformResourceRE.FindStringSubmatch(trimmed); len(match) > 2 {
			value := match[1] + "." + match[2]
			add("infra_resource", value, value, "terraform", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Terraform resource"})
		}
		if strings.HasPrefix(trimmed, "kind:") && strings.Contains(file.content, "apiVersion:") {
			kind := strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
			if kind != "" {
				add("infra_resource", "Kubernetes "+kind, "kubernetes/"+kind, "kubernetes", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "Kubernetes manifest kind"})
			}
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") && filepath.Base(file.path) == "Dockerfile" {
			image := strings.TrimSpace(trimmed[5:])
			if parts := strings.Fields(image); len(parts) > 0 {
				add("deployment", "Container base "+parts[0], parts[0], "dockerfile", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "container base image"})
			}
		}
		detectEnvironmentReference(file, lineNo, line, add)
	})
}

func detectEnvironmentReference(file scannedFile, lineNo int, line string, add addCorrelationFunc) {
	upper := strings.ToUpper(line)
	refs := []struct {
		token string
		label string
	}{
		{"DATABASE_URL", "Database URL"},
		{"REDIS_URL", "Redis URL"},
		{"S3_BUCKET", "S3 bucket"},
		{"BUCKET_NAME", "Object storage bucket"},
		{"QUEUE_URL", "Queue URL"},
		{"KAFKA_BROKERS", "Kafka brokers"},
		{"OTEL_EXPORTER", "Telemetry exporter"},
	}
	for _, ref := range refs {
		if strings.Contains(upper, ref.token) {
			add("infra_reference", ref.label, ref.token, "environment", Evidence{File: file.path, Line: lineNo, Snippet: line, Reason: "environment-backed infrastructure reference"})
		}
	}
}

func scanLines(file scannedFile, visit func(lineNo int, line string)) {
	scanner := bufio.NewScanner(strings.NewReader(file.content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		visit(lineNo, scanner.Text())
	}
}

func correlationOrder(kind string) int {
	switch kind {
	case "work_item":
		return 0
	case "service":
		return 1
	case "infra_resource":
		return 2
	case "infra_reference":
		return 3
	case "deployment":
		return 4
	case "dependency":
		return 5
	default:
		return 99
	}
}

func buildLanguageStats(files []scannedFile) []LanguageStat {
	byID := map[string]*LanguageStat{}
	totalLines := 0
	for _, file := range files {
		if file.language == "" {
			continue
		}
		def := languageDefByID(file.language)
		stat := byID[file.language]
		if stat == nil {
			stat = &LanguageStat{ID: file.language, Label: def.label, Extensions: def.extensions}
			byID[file.language] = stat
		}
		stat.Files++
		stat.Lines += file.lines
		totalLines += file.lines
	}
	out := make([]LanguageStat, 0, len(byID))
	for _, stat := range byID {
		if totalLines > 0 {
			stat.Percentage = float64(stat.Lines) / float64(totalLines) * 100
		}
		out = append(out, *stat)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func buildGraph(repoURL string, files []scannedFile, languages []LanguageStat, patterns []CodePattern, correlations []Correlation) CodeGraph {
	nodes := make([]GraphNode, 0)
	edges := make([]GraphEdge, 0)
	addedNodes := map[string]bool{}
	addedEdges := map[string]bool{}

	addNode := func(node GraphNode) {
		if node.ID == "" || addedNodes[node.ID] {
			return
		}
		addedNodes[node.ID] = true
		nodes = append(nodes, node)
	}
	addEdge := func(edge GraphEdge) {
		if edge.Source == "" || edge.Target == "" || edge.Source == edge.Target {
			return
		}
		if edge.ID == "" {
			edge.ID = stableID(edge.Source + ":" + edge.Target + ":" + edge.Type)
		}
		if addedEdges[edge.ID] {
			return
		}
		addedEdges[edge.ID] = true
		edges = append(edges, edge)
	}

	repoLabel := repoName(repoURL)
	addNode(GraphNode{ID: "repo", Type: "repo", Label: repoLabel, Group: "Repository", Metadata: map[string]interface{}{"repoUrl": repoURL}})

	for _, lang := range languages {
		id := "language:" + lang.ID
		addNode(GraphNode{ID: id, Type: "language", Label: lang.Label, Group: "Language", Metadata: map[string]interface{}{"files": lang.Files, "lines": lang.Lines, "percentage": lang.Percentage}})
		addEdge(GraphEdge{ID: stableID("repo:" + id), Source: "repo", Target: id, Type: "uses", Label: "uses"})
	}

	patternIDs := map[string]bool{}
	for _, pattern := range patterns {
		id := "pattern:" + pattern.ID
		patternIDs[pattern.ID] = true
		addNode(GraphNode{ID: id, Type: "pattern", Label: pattern.Label, Group: pattern.Category, Metadata: map[string]interface{}{"files": len(pattern.Files), "confidence": pattern.Confidence}})
		addEdge(GraphEdge{ID: stableID("repo:" + id), Source: "repo", Target: id, Type: "contains", Label: "contains"})
	}

	for _, rel := range patternRelationships {
		if !patternIDs[rel.source] || !patternIDs[rel.target] {
			continue
		}
		addEdge(GraphEdge{ID: stableID(rel.source + rel.target + rel.label), Source: "pattern:" + rel.source, Target: "pattern:" + rel.target, Type: "pattern-flow", Label: rel.label})
	}

	for _, corr := range topGraphCorrelations(correlations) {
		addNode(GraphNode{
			ID:    corr.ID,
			Type:  "correlation",
			Label: corr.Label,
			Group: correlationGroup(corr.Type),
			Metadata: map[string]interface{}{
				"type":   corr.Type,
				"value":  corr.Value,
				"source": corr.Source,
				"files":  corr.Files,
			},
		})
		addEdge(GraphEdge{ID: stableID("repo:" + corr.ID), Source: "repo", Target: corr.ID, Type: "correlates", Label: "correlates"})
		if patternID := patternForCorrelation(corr.Type); patternID != "" && patternIDs[patternID] {
			addEdge(GraphEdge{ID: stableID("pattern:" + patternID + ":" + corr.ID), Source: "pattern:" + patternID, Target: corr.ID, Type: "correlation", Label: "links"})
		}
	}

	topFiles := topGraphFiles(files)
	for _, file := range topFiles {
		fileID := "file:" + file.path
		addNode(GraphNode{
			ID:    fileID,
			Type:  "file",
			Label: filepath.Base(file.path),
			Group: "File",
			Metadata: map[string]interface{}{
				"path":     file.path,
				"language": file.language,
				"lines":    file.lines,
				"patterns": sortedPatternIDs(file.patterns),
			},
		})
		if file.language != "" {
			addEdge(GraphEdge{ID: stableID("lang:" + file.language + ":" + file.path), Source: "language:" + file.language, Target: fileID, Type: "language-file", Label: "file"})
		}
		for patternID := range file.patterns {
			addEdge(GraphEdge{ID: stableID("pattern:" + patternID + ":" + file.path), Source: "pattern:" + patternID, Target: fileID, Type: "example", Label: "example"})
		}
	}

	fileNodeSet := map[string]bool{}
	for _, file := range topFiles {
		fileNodeSet[file.path] = true
	}
	importEdges := resolveImportEdges(files, fileNodeSet)
	for _, edge := range importEdges {
		if len(edges) >= maxImportEdges+len(patterns)*2+len(languages)+1 {
			break
		}
		addEdge(edge)
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		typeOrder := map[string]int{"repo": 0, "pattern": 1, "correlation": 2, "language": 3, "file": 4}
		if typeOrder[nodes[i].Type] != typeOrder[nodes[j].Type] {
			return typeOrder[nodes[i].Type] < typeOrder[nodes[j].Type]
		}
		return nodes[i].Label < nodes[j].Label
	})
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })

	return CodeGraph{Nodes: nodes, Edges: edges}
}

var patternRelationships = []struct {
	source string
	target string
	label  string
}{
	{"entry_point", "config", "loads"},
	{"entry_point", "routes_handlers", "registers"},
	{"routes_handlers", "middleware", "passes through"},
	{"middleware", "auth", "enforces"},
	{"routes_handlers", "validation", "validates"},
	{"routes_handlers", "services", "calls"},
	{"services", "database", "reads/writes"},
	{"services", "integrations", "calls"},
	{"services", "events", "publishes"},
	{"jobs_workers", "services", "runs"},
	{"services", "notifications", "notifies"},
	{"services", "billing", "charges"},
	{"database", "migrations", "evolves"},
	{"infrastructure", "entry_point", "runs"},
	{"tests", "services", "covers"},
	{"logging", "errors", "observes"},
}

func resolveImportEdges(files []scannedFile, fileNodeSet map[string]bool) []GraphEdge {
	byPath := make(map[string]scannedFile)
	for _, file := range files {
		byPath[file.path] = file
	}
	edges := make([]GraphEdge, 0)
	seen := map[string]bool{}
	for _, file := range files {
		if !fileNodeSet[file.path] {
			continue
		}
		for _, imp := range file.imports {
			target := resolveRelativeImport(file.path, imp, byPath)
			if target == "" || !fileNodeSet[target] {
				continue
			}
			id := stableID("import:" + file.path + ":" + target)
			if seen[id] {
				continue
			}
			seen[id] = true
			edges = append(edges, GraphEdge{
				ID:     id,
				Source: "file:" + file.path,
				Target: "file:" + target,
				Type:   "imports",
				Label:  "imports",
			})
		}
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	if len(edges) > maxImportEdges {
		return edges[:maxImportEdges]
	}
	return edges
}

func resolveRelativeImport(fromPath, imp string, byPath map[string]scannedFile) string {
	imp = strings.TrimSpace(imp)
	if !strings.HasPrefix(imp, ".") {
		return ""
	}
	baseDir := filepath.Dir(fromPath)
	candidate := filepath.Clean(filepath.Join(baseDir, imp))
	candidate = filepath.ToSlash(candidate)
	for _, suffix := range []string{"", ".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".rs", ".java", "/index.ts", "/index.tsx", "/index.js", "/index.jsx", "/__init__.py"} {
		path := candidate + suffix
		if _, ok := byPath[path]; ok {
			return path
		}
	}
	return ""
}

func topGraphCorrelations(correlations []Correlation) []Correlation {
	candidates := append([]Correlation(nil), correlations...)
	sort.SliceStable(candidates, func(i, j int) bool {
		oi := correlationOrder(candidates[i].Type)
		oj := correlationOrder(candidates[j].Type)
		if oi != oj {
			return oi < oj
		}
		if len(candidates[i].Files) != len(candidates[j].Files) {
			return len(candidates[i].Files) > len(candidates[j].Files)
		}
		return candidates[i].Label < candidates[j].Label
	})
	if len(candidates) > maxCorrelationNodes {
		return candidates[:maxCorrelationNodes]
	}
	return candidates
}

func correlationGroup(kind string) string {
	switch kind {
	case "work_item":
		return "Work Items"
	case "service":
		return "Runtime"
	case "infra_resource", "infra_reference", "deployment":
		return "Infrastructure"
	case "dependency":
		return "Dependencies"
	default:
		return "Workspace Links"
	}
}

func patternForCorrelation(kind string) string {
	switch kind {
	case "dependency":
		return "integrations"
	case "infra_resource", "infra_reference", "deployment":
		return "infrastructure"
	case "service":
		return "logging"
	case "work_item":
		return "documentation"
	default:
		return ""
	}
}

func topGraphFiles(files []scannedFile) []scannedFile {
	candidates := append([]scannedFile(nil), files...)
	sort.SliceStable(candidates, func(i, j int) bool {
		ip := patternWeight(candidates[i])
		jp := patternWeight(candidates[j])
		if ip != jp {
			return ip > jp
		}
		if len(candidates[i].imports) != len(candidates[j].imports) {
			return len(candidates[i].imports) > len(candidates[j].imports)
		}
		return candidates[i].path < candidates[j].path
	})
	if len(candidates) > maxGraphFileNodes {
		return candidates[:maxGraphFileNodes]
	}
	return candidates
}

func patternWeight(file scannedFile) int {
	weight := len(file.patterns)
	for _, id := range importantPatternOrder {
		if _, ok := file.patterns[id]; ok {
			weight += 3
		}
	}
	return weight
}

func buildSummary(files []scannedFile, languages []LanguageStat, patterns []CodePattern, correlations []Correlation, graph CodeGraph) Summary {
	totalLines := 0
	sourceFiles := 0
	for _, file := range files {
		totalLines += file.lines
		if file.language != "markdown" && file.language != "yaml" {
			sourceFiles++
		}
	}
	patternSet := map[string]bool{}
	entryPoint := ""
	framework := ""
	for _, file := range files {
		for id := range file.patterns {
			patternSet[id] = true
		}
		if entryPoint == "" {
			if _, ok := file.patterns["entry_point"]; ok {
				entryPoint = file.path
			}
		}
		if framework == "" {
			framework = detectFramework(file)
		}
	}
	primary := ""
	if len(languages) > 0 {
		primary = languages[0].Label
	}
	return Summary{
		PrimaryLanguage:  primary,
		TotalFiles:       len(files),
		SourceFiles:      sourceFiles,
		TotalLines:       totalLines,
		PatternCount:     len(patterns),
		CorrelationCount: len(correlations),
		ConnectionCount:  len(graph.Edges),
		EntryPoint:       entryPoint,
		Framework:        framework,
		HasAuth:          patternSet["auth"],
		HasDatabase:      patternSet["database"],
		HasMiddleware:    patternSet["middleware"],
		HasTests:         patternSet["tests"],
	}
}

func detectFramework(file scannedFile) string {
	if filepath.Base(file.path) == "package.json" {
		content := strings.ToLower(file.content)
		for _, fw := range []string{"next", "express", "fastify", "react", "vite", "nuxt", "svelte"} {
			if strings.Contains(content, `"`+fw+`"`) {
				return fw
			}
		}
	}
	if filepath.Base(file.path) == "go.mod" {
		content := strings.ToLower(file.content)
		for _, fw := range []string{"gin-gonic", "gofiber", "go-chi", "echo"} {
			if strings.Contains(content, fw) {
				return fw
			}
		}
	}
	if filepath.Base(file.path) == "requirements.txt" || filepath.Base(file.path) == "pyproject.toml" {
		content := strings.ToLower(file.content)
		for _, fw := range []string{"fastapi", "flask", "django", "streamlit"} {
			if strings.Contains(content, fw) {
				return fw
			}
		}
	}
	return ""
}

func detectImports(path, content string) []string {
	imports := make([]string, 0)
	for _, match := range relativeImportRE.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			imports = appendUnique(imports, match[1])
		}
	}
	if strings.HasSuffix(path, ".py") {
		for _, match := range pythonImportRE.FindAllStringSubmatch(content, -1) {
			if len(match) > 1 {
				imports = appendUnique(imports, strings.ReplaceAll(match[1], ".", "/"))
			}
		}
	}
	sort.Strings(imports)
	if len(imports) > 30 {
		return imports[:30]
	}
	return imports
}

func readTextFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	limited := io.LimitReader(f, maxFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if bytesContainNUL(data) {
		return "", fmt.Errorf("binary file")
	}
	if len(data) > maxFileBytes {
		data = data[:maxFileBytes]
	}
	return string(data), nil
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", ".next", ".nuxt", "target", "__pycache__", ".cache", ".turbo", ".venv", "venv", ".idea", ".vscode", "coverage", ".pytest_cache":
		return true
	default:
		return false
	}
}

func shouldSkipFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".ds_store") {
		return true
	}
	for _, suffix := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".icns", ".pdf", ".zip", ".gz", ".tar", ".tgz", ".mp4", ".mov", ".lock"} {
		if strings.HasSuffix(lower, suffix) && lower != "package-lock.json" {
			return true
		}
	}
	return false
}

func languageForPath(path string) string {
	base := filepath.Base(path)
	switch base {
	case "Dockerfile", "Makefile":
		return "shell"
	case "package.json", "tsconfig.json", "composer.json":
		return "javascript"
	case "go.mod", "go.sum":
		return "go"
	case "Cargo.toml":
		return "rust"
	case "pom.xml", "build.gradle", "build.gradle.kts":
		return "java"
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".h" {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "cpp") || strings.Contains(lower, "cxx") || strings.Contains(lower, "include/") {
			return "cpp"
		}
	}
	return languageByExtension[ext]
}

func buildLanguageIndex() map[string]string {
	out := map[string]string{}
	for _, def := range languageDefs {
		for _, ext := range def.extensions {
			out[strings.ToLower(ext)] = def.id
		}
	}
	return out
}

func supportedLanguageSpecs() []LanguageSpec {
	primary := []string{"javascript", "typescript", "python", "java", "go", "rust", "csharp", "cpp", "c", "php"}
	out := make([]LanguageSpec, 0, len(primary))
	for _, id := range primary {
		def := languageDefByID(id)
		out = append(out, LanguageSpec{ID: def.id, Label: def.label, Extensions: def.extensions})
	}
	return out
}

func languageDefByID(id string) languageDef {
	for _, def := range languageDefs {
		if def.id == id {
			return def
		}
	}
	return languageDef{id: id, label: strings.Title(id)}
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func trimSnippet(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 180 {
		return s[:177] + "..."
	}
	return s
}

func firstNonEmptyLine(content string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			return line
		}
	}
	return ""
}

func appendLimitedEvidence(items []Evidence, ev Evidence, max int) []Evidence {
	if trimSnippet(ev.Snippet) == "" {
		return items
	}
	for _, existing := range items {
		if existing.Line == ev.Line && existing.File == ev.File && existing.Reason == ev.Reason {
			return items
		}
	}
	if len(items) >= max {
		return items
	}
	return append(items, ev)
}

func confidenceForPattern(files, evidence int) float64 {
	score := 0.42 + float64(files)*0.035 + float64(evidence)*0.03
	if score > 0.96 {
		return 0.96
	}
	if score < 0.35 {
		return 0.35
	}
	return score
}

func patternOrder(id string) int {
	for i, item := range importantPatternOrder {
		if item == id {
			return i
		}
	}
	return len(importantPatternOrder) + 1
}

func sortedPatternIDs(patterns map[string][]Evidence) []string {
	out := make([]string, 0, len(patterns))
	for id := range patterns {
		out = append(out, id)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi := patternOrder(out[i])
		oj := patternOrder(out[j])
		if oi != oj {
			return oi < oj
		}
		return out[i] < out[j]
	})
	return out
}

func stableID(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func repoName(repoURL string) string {
	clean := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")
	clean = strings.TrimSuffix(clean, "/")
	if clean == "" {
		return "Repository"
	}
	parts := strings.Split(clean, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return parts[len(parts)-1]
}

func bytesContainNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func appendUnique(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func languageFindings(languages []LanguageStat) []string {
	out := make([]string, 0, 3)
	for i, lang := range languages {
		if i >= 3 {
			break
		}
		out = append(out, fmt.Sprintf("%s: %d files / %d lines", lang.Label, lang.Files, lang.Lines))
	}
	return out
}

func topPatternFindings(patterns []CodePattern) []string {
	out := make([]string, 0, 5)
	for i, pattern := range patterns {
		if i >= 5 {
			break
		}
		out = append(out, fmt.Sprintf("%s: %d files", pattern.Label, len(pattern.Files)))
	}
	return out
}

func correlationFindings(correlations []Correlation) []string {
	counts := map[string]int{}
	for _, corr := range correlations {
		counts[corr.Type]++
	}
	order := []string{"work_item", "service", "infra_resource", "infra_reference", "deployment", "dependency"}
	out := make([]string, 0, len(order))
	for _, kind := range order {
		if counts[kind] > 0 {
			out = append(out, fmt.Sprintf("%s: %d", kind, counts[kind]))
		}
	}
	if len(out) > 5 {
		return out[:5]
	}
	return out
}

func dependencyFindings(graph CodeGraph) []string {
	counts := map[string]int{}
	for _, edge := range graph.Edges {
		counts[edge.Type]++
	}
	out := make([]string, 0, 4)
	for _, typ := range []string{"pattern-flow", "example", "imports", "language-file"} {
		if counts[typ] > 0 {
			out = append(out, fmt.Sprintf("%s: %d", typ, counts[typ]))
		}
	}
	return out
}

func surfaceSummary(summary Summary) string {
	parts := make([]string, 0, 3)
	if summary.HasAuth {
		parts = append(parts, "auth")
	}
	if summary.HasDatabase {
		parts = append(parts, "database")
	}
	if summary.HasMiddleware {
		parts = append(parts, "middleware")
	}
	if len(parts) == 0 {
		return "No auth, database, or middleware surface detected in the first scan"
	}
	return "Detected " + strings.Join(parts, ", ")
}

func surfaceFindings(patterns []CodePattern) []string {
	want := map[string]bool{"auth": true, "database": true, "middleware": true, "validation": true, "integrations": true}
	out := make([]string, 0, 5)
	for _, pattern := range patterns {
		if want[pattern.ID] {
			out = append(out, fmt.Sprintf("%s examples: %d", pattern.Label, len(pattern.Files)))
		}
	}
	return out
}
