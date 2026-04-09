package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/dbcontext"
	"github.com/bgdnvk/clanker/internal/digitalocean"
	"github.com/bgdnvk/clanker/internal/gcp"
	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/bgdnvk/clanker/internal/hetzner"
	"github.com/spf13/viper"
)

const maxDomainAgentSectionChars = 8000

type domainContextSection struct {
	Title   string
	Content string
}

func handleDatabaseQuery(ctx context.Context, question string, debug bool, dbConnection string) error {
	routingQuestion := questionForRouting(question)
	sections, warnings := collectDatabaseAgentContext(ctx, routingQuestion, dbConnection, debug)
	return runDomainAgentQuery(ctx, "database", question, sections, warnings, debug)
}

func handleCICDQuery(ctx context.Context, question string, debug bool) error {
	routingQuestion := questionForRouting(question)
	sections, warnings := collectCICDAgentContext(ctx, routingQuestion, debug)
	return runDomainAgentQuery(ctx, "cicd", question, sections, warnings, debug)
}

func collectDatabaseAgentContext(ctx context.Context, question string, dbConnection string, debug bool) ([]domainContextSection, []string) {
	sections := make([]domainContextSection, 0, 8)
	warnings := make([]string, 0, 8)

	if dbInfo, err := dbcontext.BuildRelevantContext(ctx, question, dbConnection); err != nil {
		warnings = appendDomainWarning(warnings, "Configured database connections", err)
	} else {
		sections = appendDomainSection(sections, "Configured Database Connections", dbInfo)
	}

	if shouldQueryDomainProvider(question, "aws") && hasAWSDomainAccess() {
		awsProfile := resolveAWSProfile("")
		awsRegion := resolveAWSRegion(ctx, awsProfile)
		awsClient, err := aws.NewClientWithProfileAndDebug(ctx, awsProfile, debug)
		if err != nil {
			warnings = appendDomainWarning(warnings, "AWS database inventory", err)
		} else {
			awsInfo, awsErr := awsClient.ExecuteOperationsWithAWSProfile(ctx, []aws.LLMOperation{
				{Operation: "list_rds_instances", Reason: "Get managed SQL inventory", Parameters: map[string]interface{}{}},
				{Operation: "list_dynamodb_tables", Reason: "Get NoSQL table inventory", Parameters: map[string]interface{}{}},
				{Operation: "list_elasticache_clusters", Reason: "Get cache inventory", Parameters: map[string]interface{}{}},
			}, awsProfile, awsRegion)
			if awsErr != nil {
				warnings = appendDomainWarning(warnings, "AWS database inventory", awsErr)
			} else {
				sections = appendDomainSection(sections, "AWS Databases", awsInfo)
			}
		}
	}

	if shouldQueryDomainProvider(question, "gcp") {
		projectID := strings.TrimSpace(gcp.ResolveProjectID())
		if projectID != "" {
			gcpClient, err := gcp.NewClient(projectID, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "GCP database inventory", err)
			} else {
				gcpInfo, gcpErr := gcpClient.GetRelevantContext(ctx, "cloud sql firestore spanner bigtable memorystore redis memcache database")
				if gcpErr != nil {
					warnings = appendDomainWarning(warnings, "GCP database inventory", gcpErr)
				} else {
					sections = appendDomainSection(sections, "GCP Databases", gcpInfo)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "azure") {
		subscriptionID := strings.TrimSpace(azure.ResolveSubscriptionID())
		if subscriptionID != "" {
			azureClient, err := azure.NewClient(subscriptionID, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "Azure database inventory", err)
			} else {
				azureInfo, azureErr := azureClient.GetRelevantContext(ctx, "azure cosmos db database")
				if azureErr != nil {
					warnings = appendDomainWarning(warnings, "Azure database inventory", azureErr)
				} else {
					sections = appendDomainSection(sections, "Azure Databases", azureInfo)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "digitalocean") {
		apiToken := strings.TrimSpace(digitalocean.ResolveAPIToken())
		if apiToken != "" {
			doClient, err := digitalocean.NewClient(apiToken, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "DigitalOcean database inventory", err)
			} else {
				doInfo, doErr := doClient.GetRelevantContext(ctx, "databases postgres mysql redis mongo")
				if doErr != nil {
					warnings = appendDomainWarning(warnings, "DigitalOcean database inventory", doErr)
				} else {
					sections = appendDomainSection(sections, "DigitalOcean Databases", doInfo)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "cloudflare") {
		apiToken := strings.TrimSpace(cloudflare.ResolveAPIToken())
		if apiToken != "" {
			cfClient, err := cloudflare.NewClient(cloudflare.ResolveAccountID(), apiToken, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "Cloudflare database inventory", err)
			} else {
				d1Info, d1Err := cfClient.RunWranglerWithContext(ctx, "d1", "list")
				if d1Err != nil {
					warnings = appendDomainWarning(warnings, "Cloudflare D1 inventory", d1Err)
				} else {
					sections = appendDomainSection(sections, "Cloudflare D1 Databases", d1Info)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "hetzner") && hasHetznerDomainAccess() {
		warnings = append(warnings, "Hetzner database inventory is not implemented in the current CLI integrations")
	}

	return sections, warnings
}

func collectCICDAgentContext(ctx context.Context, question string, debug bool) ([]domainContextSection, []string) {
	sections := make([]domainContextSection, 0, 8)
	warnings := make([]string, 0, 8)

	if shouldQueryDomainProvider(question, "github") {
		githubClient := ghclient.NewClient(viper.GetString("github.token"), viper.GetString("github.owner"), viper.GetString("github.repo"))
		githubInfo, err := githubClient.GetRelevantContext(ctx, "github actions workflow runs runners repository")
		if err != nil {
			warnings = appendDomainWarning(warnings, "GitHub CI/CD inventory", err)
		} else {
			sections = appendDomainSection(sections, "GitHub Actions", githubInfo)
		}
	}

	if shouldQueryDomainProvider(question, "aws") && hasAWSDomainAccess() {
		awsProfile := resolveAWSProfile("")
		awsRegion := resolveAWSRegion(ctx, awsProfile)
		awsClient, err := aws.NewClientWithProfileAndDebug(ctx, awsProfile, debug)
		if err != nil {
			warnings = appendDomainWarning(warnings, "AWS CI/CD inventory", err)
		} else {
			awsInfo, awsErr := awsClient.ExecuteOperationsWithAWSProfile(ctx, []aws.LLMOperation{
				{Operation: "list_codebuild_projects", Reason: "Get build project inventory", Parameters: map[string]interface{}{}},
				{Operation: "list_codepipelines", Reason: "Get deployment pipeline inventory", Parameters: map[string]interface{}{}},
			}, awsProfile, awsRegion)
			if awsErr != nil {
				warnings = appendDomainWarning(warnings, "AWS CI/CD inventory", awsErr)
			} else {
				sections = appendDomainSection(sections, "AWS CI/CD", awsInfo)
			}
		}
	}

	if shouldQueryDomainProvider(question, "gcp") {
		projectID := strings.TrimSpace(gcp.ResolveProjectID())
		if projectID != "" {
			gcpClient, err := gcp.NewClient(projectID, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "GCP CI/CD inventory", err)
			} else {
				gcpInfo, gcpErr := gcpClient.GetRelevantContext(ctx, "cloud build cloud deploy build triggers delivery pipelines")
				if gcpErr != nil {
					warnings = appendDomainWarning(warnings, "GCP CI/CD inventory", gcpErr)
				} else {
					sections = appendDomainSection(sections, "GCP CI/CD", gcpInfo)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "digitalocean") {
		apiToken := strings.TrimSpace(digitalocean.ResolveAPIToken())
		if apiToken != "" {
			doClient, err := digitalocean.NewClient(apiToken, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "DigitalOcean CI/CD inventory", err)
			} else {
				doInfo, doErr := doClient.GetRelevantContext(ctx, "apps app platform deployment")
				if doErr != nil {
					warnings = appendDomainWarning(warnings, "DigitalOcean CI/CD inventory", doErr)
				} else {
					sections = appendDomainSection(sections, "DigitalOcean Deployments", doInfo)
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "cloudflare") {
		apiToken := strings.TrimSpace(cloudflare.ResolveAPIToken())
		if apiToken != "" {
			cfClient, err := cloudflare.NewClient(cloudflare.ResolveAccountID(), apiToken, debug)
			if err != nil {
				warnings = appendDomainWarning(warnings, "Cloudflare CI/CD inventory", err)
			} else {
				deployments, deployErr := cfClient.RunWranglerWithContext(ctx, "deployments", "list")
				if deployErr != nil {
					warnings = appendDomainWarning(warnings, "Cloudflare deployments", deployErr)
				} else {
					sections = appendDomainSection(sections, "Cloudflare Deployments", deployments)
				}
				if accountID := strings.TrimSpace(cfClient.GetAccountID()); accountID != "" {
					pages, pagesErr := cfClient.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/accounts/%s/pages/projects", accountID), "")
					if pagesErr != nil {
						warnings = appendDomainWarning(warnings, "Cloudflare Pages projects", pagesErr)
					} else {
						sections = appendDomainSection(sections, "Cloudflare Pages Projects", pages)
					}
				}
			}
		}
	}

	if shouldQueryDomainProvider(question, "azure") && strings.TrimSpace(azure.ResolveSubscriptionID()) != "" {
		warnings = append(warnings, "Azure DevOps pipeline inventory is not implemented in the current CLI integrations")
	}
	if shouldQueryDomainProvider(question, "hetzner") && hasHetznerDomainAccess() {
		warnings = append(warnings, "Hetzner build or deployment inventory is not implemented in the current CLI integrations")
	}

	return sections, warnings
}

func runDomainAgentQuery(ctx context.Context, domain string, question string, sections []domainContextSection, warnings []string, debug bool) error {
	aiClient := newConfiguredAIClient(debug)
	prompt := buildDomainAgentPrompt(domain, question, sections, warnings)
	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return fmt.Errorf("failed to get %s agent response: %w", domain, err)
	}

	fmt.Println(response)
	return nil
}

func buildDomainAgentPrompt(domain string, question string, sections []domainContextSection, warnings []string) string {
	var instructions string
	switch domain {
	case "database":
		instructions = `You are Clanker's database agent.

Use the evidence below to answer questions about SQL and NoSQL systems across configured local connections and cloud-managed databases.

Coverage in this repo can include:
- configured SQL connections from clanker config
- AWS RDS, DynamoDB, ElastiCache
- GCP Cloud SQL, Firestore, Spanner, Bigtable, Memorystore
- Azure Cosmos DB
- DigitalOcean managed databases
- Cloudflare D1

Rules:
- Separate observed facts from recommendations.
- If a provider or engine is missing, say it was not available from current credentials or integrations.
- Do not invent schema details, table names, counts, or status.
- Be concrete about engines, tables, columns, state, and obvious gaps.`
	default:
		instructions = `You are Clanker's CI/CD agent.

Use the evidence below to answer questions about build and deployment systems across repositories and cloud providers.

Coverage in this repo can include:
- GitHub Actions workflows, runs, and runners
- AWS CodeBuild and CodePipeline
- GCP Cloud Build and Cloud Deploy
- Cloudflare deployments and Pages projects
- DigitalOcean App Platform deployments

Rules:
- Separate observed facts from recommendations.
- If a provider is missing, say it was not available from current credentials or integrations.
- Do not invent workflow names, pipeline state, run results, or deployment history.
- Be concrete about failing runs, pipeline state, runner availability, and missing coverage.`
	}

	b := &strings.Builder{}
	b.WriteString(instructions)
	b.WriteString("\n\nEvidence:\n")
	if len(sections) == 0 {
		b.WriteString("No live domain evidence was collected.\n")
	}
	for _, section := range sections {
		b.WriteString(section.Title)
		b.WriteString(":\n")
		b.WriteString(section.Content)
		b.WriteString("\n\n")
	}
	if len(warnings) > 0 {
		b.WriteString("Collection Warnings:\n")
		for i, warningText := range warnings {
			if i >= 10 {
				b.WriteString("- additional warnings omitted\n")
				break
			}
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(warningText))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("User Question: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nAnswer as a concise operator report. Start with what you found, then note gaps or next checks only if needed.")
	return b.String()
}

func newConfiguredAIClient(debug bool) *ai.Client {
	provider := strings.TrimSpace(viper.GetString("ai.default_provider"))
	if provider == "" {
		provider = "openai"
	}

	var apiKey string
	switch provider {
	case "gemini":
		apiKey = ""
	case "gemini-api":
		apiKey = resolveGeminiAPIKey("")
	case "openai":
		apiKey = resolveOpenAIKey("")
	case "anthropic":
		apiKey = resolveAnthropicKey("")
	case "deepseek":
		apiKey = resolveDeepSeekKey("")
	case "cohere":
		apiKey = resolveCohereKey("")
	case "minimax":
		apiKey = resolveMiniMaxKey("")
	case "github-models":
		apiKey = ""
	default:
		apiKey = viper.GetString("ai.api_key")
	}

	return ai.NewClient(provider, apiKey, debug, provider)
}

func appendDomainSection(sections []domainContextSection, title string, content string) []domainContextSection {
	trimmed := truncateDomainSection(content)
	if trimmed == "" {
		return sections
	}
	return append(sections, domainContextSection{Title: title, Content: trimmed})
}

func appendDomainWarning(warnings []string, title string, err error) []string {
	if err == nil {
		return warnings
	}
	return append(warnings, fmt.Sprintf("%s: %v", title, err))
}

func truncateDomainSection(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= maxDomainAgentSectionChars {
		return trimmed
	}
	return trimmed[:maxDomainAgentSectionChars] + "\n...<truncated>"
}

func hasAWSDomainAccess() bool {
	if strings.TrimSpace(os.Getenv("AWS_PROFILE")) != "" || strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) != "" || strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "" {
		return true
	}
	defaultEnv := strings.TrimSpace(viper.GetString("infra.default_environment"))
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	if strings.TrimSpace(viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))) != "" {
		return true
	}
	return strings.TrimSpace(viper.GetString("aws.default_profile")) != ""
}

func hasHetznerDomainAccess() bool {
	return strings.TrimSpace(hetzner.ResolveAPIToken()) != ""
}

func shouldQueryDomainProvider(question string, provider string) bool {
	providerSignals := map[string][]string{
		"aws":          {"aws", "rds", "dynamodb", "elasticache", "codebuild", "codepipeline"},
		"gcp":          {"gcp", "google cloud", "cloud sql", "firestore", "spanner", "bigtable", "cloud build", "cloud deploy"},
		"azure":        {"azure", "cosmos", "azure devops"},
		"cloudflare":   {"cloudflare", "workers", "pages", "d1"},
		"digitalocean": {"digitalocean", "digital ocean", "doctl", "doks", "app platform"},
		"hetzner":      {"hetzner", "hcloud"},
		"github":       {"github", "github actions", "workflow", "workflows", "runner", "runners"},
	}

	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return true
	}

	anyScoped := false
	for _, signals := range providerSignals {
		if containsAnyPhrase(lower, signals...) {
			anyScoped = true
			break
		}
	}
	if !anyScoped {
		return true
	}

	signals, ok := providerSignals[provider]
	if !ok {
		return true
	}
	return containsAnyPhrase(lower, signals...)
}

func shouldRouteToDatabaseAgent(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if containsAnyPhrase(lower, "pod", "pods", "deployment", "deployments", "statefulset", "daemonset", "helm", "kubectl", "namespace") &&
		!containsAnyPhrase(lower, "database", "schema", "sql", "nosql", "table", "tables", "column", "columns", "migration", "migrations", "connection", "query") {
		return false
	}
	if containsAnyPhrase(lower, "github actions", "workflow", "workflow run", "codebuild", "codepipeline", "cloud build", "cloud deploy") &&
		!containsAnyPhrase(lower, "database", "schema", "sql", "nosql", "table", "tables", "column", "columns", "migration", "postgres", "mysql", "sqlite", "supabase", "neon", "dynamodb", "firestore", "spanner", "bigtable", "cosmos", "d1", "redis", "mongo") {
		return false
	}
	if containsAnyPhrase(lower, "schema", "schemas", "sql query", "sql", "nosql", "migration", "migrations", "foreign key", "primary key", "index", "indexes", "connection string", "database connection", "db connection") {
		return true
	}
	if containsAnyPhrase(lower, "table", "tables", "column", "columns") &&
		containsAnyPhrase(lower, "database", "schema", "sql", "query", "postgres", "mysql", "sqlite", "supabase", "neon", "dynamodb", "firestore", "spanner", "bigtable", "cosmos", "d1", "redis", "mongo") {
		return true
	}
	return containsAnyPhrase(lower,
		"database", "databases", "postgres", "postgresql", "mysql", "mariadb", "sqlite", "sqlite3",
		"supabase", "neon", "mongo", "mongodb", "redis", "dynamodb", "rds", "firestore", "cloud sql",
		"spanner", "bigtable", "cosmos", "cosmos db", "d1")
}

func shouldRouteToCICDAgent(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if containsAnyPhrase(lower, "schema", "schemas", "sql query", "migration", "migrations", "database connection") &&
		!containsAnyPhrase(lower, "workflow", "workflows", "pipeline", "pipelines", "github actions", "codebuild", "codepipeline", "cloud build", "cloud deploy") {
		return false
	}
	if containsAnyPhrase(lower,
		"cicd", "ci/cd", "ci cd", "github actions", "workflow", "workflows", "workflow run", "workflow runs",
		"runner", "runners", "pipeline", "pipelines", "build trigger", "build triggers", "codebuild",
		"codepipeline", "cloud build", "cloud deploy", "delivery pipeline", "delivery pipelines", "pages deployment") {
		return true
	}
	return containsAnyPhrase(lower, "build status", "deploy status", "deployment status", "failed build", "failing pipeline", "release pipeline")
}
