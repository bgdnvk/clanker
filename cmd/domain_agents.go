package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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

const (
	maxDomainAgentSectionChars   = 8000
	maxDatabaseQueryPromptRows   = 12
	defaultDatabasePreviewRows   = 20
	maxDeterministicTableQueries = 3
	runtimeDatabaseModeEnv       = "CLANKER_RUNTIME_DB_MODE"
	runtimeDatabaseResourceEnv   = "CLANKER_RUNTIME_DB_RESOURCE_CONTEXT"
	runtimeGitHubTrackedReposEnv = "CLANKER_RUNTIME_GITHUB_TRACKED_REPOS_JSON"
)

type domainContextSection struct {
	Title   string
	Content string
}

type databaseReadPlan struct {
	ShouldQuery bool   `json:"should_query"`
	SQL         string `json:"sql"`
	Reason      string `json:"reason"`
	Target      string `json:"-"`
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

func handleSoftwareBlocksQuery(ctx context.Context, question string, debug bool) error {
	aiClient := newConfiguredAIClient(debug)
	response, err := aiClient.AskPrompt(ctx, buildSoftwareBlocksAgentPrompt(question))
	if err != nil {
		return fmt.Errorf("failed to get software-blocks agent response: %w", err)
	}

	fmt.Println(response)
	return nil
}

func handleDataFlowQuery(ctx context.Context, question string, debug bool) error {
	aiClient := newConfiguredAIClient(debug)
	response, err := aiClient.AskPrompt(ctx, buildDataFlowAgentPrompt(question))
	if err != nil {
		return fmt.Errorf("failed to get data_flow agent response: %w", err)
	}

	fmt.Println(response)
	return nil
}

func configuredDatabaseAgentMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(runtimeDatabaseModeEnv))) {
	case "inventory":
		return "inventory"
	case "query":
		return "query"
	default:
		return ""
	}
}

func normalizeGitHubTrackedRepoNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		owner, repo, ok := splitGitHubTrackedRepo(value)
		if !ok {
			continue
		}
		fullName := owner + "/" + repo
		if _, exists := seen[fullName]; exists {
			continue
		}
		seen[fullName] = struct{}{}
		normalized = append(normalized, fullName)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func splitGitHubTrackedRepo(value string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(parts[1])
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

func configuredGitHubTrackedRepos() []string {
	raw := strings.TrimSpace(os.Getenv(runtimeGitHubTrackedReposEnv))
	if raw == "" {
		return nil
	}
	var repos []string
	if err := json.Unmarshal([]byte(raw), &repos); err != nil {
		return nil
	}
	return normalizeGitHubTrackedRepoNames(repos)
}

func configuredGitHubReposForCICDAgent() []string {
	if tracked := configuredGitHubTrackedRepos(); len(tracked) > 0 {
		return tracked
	}
	owner := strings.TrimSpace(viper.GetString("github.owner"))
	repo := strings.TrimSpace(viper.GetString("github.repo"))
	if owner != "" && repo != "" {
		return []string{owner + "/" + repo}
	}
	return nil
}

func buildGitHubCICDContextQuestion(question string) string {
	base := strings.TrimSpace(question)
	suffix := "github actions workflow runs runners jobs failed steps annotations artifacts logs"
	if base == "" {
		return suffix
	}
	return base + " " + suffix
}

func hasGitHubActionsEvidence(info string) bool {
	lower := strings.ToLower(strings.TrimSpace(info))
	if lower == "" {
		return false
	}
	return !(strings.Contains(lower, "no github actions workflows found") &&
		strings.Contains(lower, "no recent workflow runs found") &&
		strings.Contains(lower, "no self-hosted runners found"))
}

func shouldAnalyzeAllConfiguredDatabases(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if !containsAnyPhrase(lower, "my databases", "my dbs", "all databases", "all dbs", "across my databases", "across my dbs") {
		return false
	}
	return containsAnyPhrase(lower,
		"what's in", "whats in", "what is in", "inside", "contents", "content",
		"inspect", "analyze", "analyse", "explore", "tell me about", "what do they contain",
		"what's in them", "what is in them")
}

func collectDatabaseAgentContext(ctx context.Context, question string, dbConnection string, debug bool) ([]domainContextSection, []string) {
	sections := make([]domainContextSection, 0, 8)
	warnings := make([]string, 0, 8)
	requestedMode := configuredDatabaseAgentMode()
	inventoryMode := requestedMode == "inventory"
	if requestedMode == "" {
		inventoryMode = isDatabaseInfrastructureInventoryQuestion(question)
	}
	swarmMode := inventoryMode && shouldAnalyzeAllConfiguredDatabases(question)

	if inventoryMode {
		if dbInfo, err := buildDatabaseConnectionInventoryContext(ctx); err != nil {
			warnings = appendDomainWarning(warnings, "Configured database connections", err)
		} else {
			sections = appendDomainSection(sections, "Configured Database Connections", dbInfo)
		}
	} else {
		if dbInfo, err := dbcontext.BuildRelevantContext(ctx, question, dbConnection); err != nil {
			warnings = appendDomainWarning(warnings, "Configured database connections", err)
		} else {
			sections = appendDomainSection(sections, "Configured Database Connections", dbInfo)
		}
	}

	if swarmMode {
		if inspectionSections, inspectionWarnings := buildConfiguredDatabaseInspectionSections(ctx); len(inspectionSections) > 0 || len(inspectionWarnings) > 0 {
			sections = append(sections, inspectionSections...)
			warnings = append(warnings, inspectionWarnings...)
		}
	}

	if resourceContext := strings.TrimSpace(os.Getenv(runtimeDatabaseResourceEnv)); resourceContext != "" {
		sections = appendDomainSection(sections, "Database Estate Resources", resourceContext)
	}

	if requestedMode != "inventory" {
		if querySections, queryWarnings := collectDatabaseQueryResults(ctx, question, dbConnection, debug); len(querySections) > 0 || len(queryWarnings) > 0 {
			sections = append(sections, querySections...)
			warnings = append(warnings, queryWarnings...)
		}
	} else if swarmMode {
		if querySections, queryWarnings := collectAllDatabaseQueryResults(ctx, question, dbConnection, debug); len(querySections) > 0 || len(queryWarnings) > 0 {
			sections = append(sections, querySections...)
			warnings = append(warnings, queryWarnings...)
		}
	}

	queryAllProviders := inventoryMode

	if (queryAllProviders || shouldQueryDomainProvider(question, "aws")) && hasAWSDomainAccess() {
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

	if queryAllProviders || shouldQueryDomainProvider(question, "gcp") {
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

	if queryAllProviders || shouldQueryDomainProvider(question, "azure") {
		azureClient := azure.NewClientWithOptionalSubscription(strings.TrimSpace(azure.ResolveSubscriptionID()), debug)
		azureInfo, azureErr := azureClient.GetRelevantContext(ctx, "azure cosmos db azure sql sql database postgresql flexible server mysql flexible server redis database")
		if azureErr != nil {
			warnings = appendDomainWarning(warnings, "Azure database inventory", azureErr)
		} else {
			sections = appendDomainSection(sections, "Azure Databases", azureInfo)
		}
	}

	if queryAllProviders || shouldQueryDomainProvider(question, "digitalocean") {
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

	if queryAllProviders || shouldQueryDomainProvider(question, "cloudflare") {
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

	if (queryAllProviders || shouldQueryDomainProvider(question, "hetzner")) && hasHetznerDomainAccess() {
		warnings = appendUnsupportedFeature(warnings, "Hetzner", "database inventory")
	}

	return sections, warnings
}

func buildDatabaseConnectionInventoryContext(ctx context.Context) (string, error) {
	connections, defaultName, err := dbcontext.ListConnections()
	if err != nil {
		return "", err
	}

	b := &strings.Builder{}
	if len(connections) == 0 {
		b.WriteString("Configured Database Connections:\n- none configured\n")
		return b.String(), nil
	}

	b.WriteString(fmt.Sprintf("Configured Database Connections (default: %s):\n", defaultName))
	for _, connection := range connections {
		marker := ""
		if connection.Name == defaultName {
			marker = " (default)"
		}
		status := "status=unknown"
		inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		inspection, inspectErr := dbcontext.Inspect(inspectCtx, connection.Name)
		cancel()
		if inspectErr != nil {
			status = fmt.Sprintf("status=unavailable error=%q", strings.TrimSpace(inspectErr.Error()))
		} else {
			status = fmt.Sprintf("status=reachable ping=%dms", inspection.PingMillis)
			if strings.TrimSpace(inspection.Version) != "" {
				status += fmt.Sprintf(" engine=%q", strings.TrimSpace(inspection.Version))
			}
			if strings.TrimSpace(inspection.CurrentDatabase) != "" {
				status += fmt.Sprintf(" database=%q", strings.TrimSpace(inspection.CurrentDatabase))
			}
		}

		b.WriteString(fmt.Sprintf("- %s%s [%s] %s %s\n", connection.Name, marker, connection.Kind(), connection.Target(), status))
		if connection.Description != "" {
			b.WriteString(fmt.Sprintf("  Description: %s\n", connection.Description))
		}
	}

	return b.String(), nil
}

func buildConfiguredDatabaseInspectionSections(ctx context.Context) ([]domainContextSection, []string) {
	connections, defaultName, err := dbcontext.ListConnections()
	if err != nil {
		return nil, []string{fmt.Sprintf("Configured database inspection: %v", err)}
	}
	if len(connections) == 0 {
		return nil, nil
	}

	sections := make([]domainContextSection, 0, len(connections))
	warnings := make([]string, 0, len(connections))
	for _, connection := range connections {
		inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		inspection, inspectErr := dbcontext.Inspect(inspectCtx, connection.Name)
		cancel()
		if inspectErr != nil {
			warnings = append(warnings, fmt.Sprintf("Configured database inspection (%s): %v", connection.Name, inspectErr))
			continue
		}

		title := fmt.Sprintf("Database Inspection (%s)", connection.Name)
		if connection.Name == defaultName {
			title = fmt.Sprintf("Database Inspection (%s, default)", connection.Name)
		}
		content := fmt.Sprintf("Connection: %s [%s] %s\n%s", connection.Name, connection.Kind(), connection.Target(), formatInspectionForQueryPlanning(inspection))
		sections = appendDomainSection(sections, title, content)
	}

	return sections, warnings
}

func collectDatabaseQueryResults(ctx context.Context, question string, dbConnection string, debug bool) ([]domainContextSection, []string) {
	return newDatabaseQuerySubAgent(debug).collectQueryResults(ctx, question, dbConnection, false)
}

func collectAllDatabaseQueryResults(ctx context.Context, question string, dbConnection string, debug bool) ([]domainContextSection, []string) {
	return newDatabaseQuerySubAgent(debug).collectQueryResults(ctx, question, dbConnection, true)
}

func selectDatabaseQueryConnections(connections []dbcontext.Connection, question string, explicitConnection string) ([]dbcontext.Connection, error) {
	trimmedConnection := strings.TrimSpace(explicitConnection)
	if trimmedConnection != "" {
		connection, err := dbcontext.ResolveConnection(trimmedConnection)
		if err != nil {
			return nil, err
		}
		return []dbcontext.Connection{connection}, nil
	}

	lowerQuestion := strings.ToLower(strings.TrimSpace(question))
	if lowerQuestion == "" {
		return connections, nil
	}

	matches := make([]dbcontext.Connection, 0, len(connections))
	for _, connection := range connections {
		if databaseConnectionMatchesQuestion(connection, lowerQuestion) {
			matches = append(matches, connection)
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}

	return connections, nil
}

func databaseConnectionMatchesQuestion(connection dbcontext.Connection, lowerQuestion string) bool {
	fields := []string{
		connection.Name,
		connection.Driver,
		connection.Vendor,
		connection.Host,
		connection.Database,
		connection.Description,
		connection.Kind(),
		connection.Target(),
	}
	for _, field := range fields {
		trimmed := strings.ToLower(strings.TrimSpace(field))
		if trimmed == "" {
			continue
		}
		if strings.Contains(lowerQuestion, trimmed) {
			return true
		}
	}
	return false
}

func planDeterministicDatabaseReadQueries(question string, inspection dbcontext.Inspection) ([]databaseReadPlan, bool) {
	lowerQuestion := strings.ToLower(strings.TrimSpace(question))
	if lowerQuestion == "" {
		return nil, false
	}

	if hasDeterministicSchemaInventoryIntent(lowerQuestion) {
		if query, ok := deterministicSchemaInventoryQuery(inspection.Connection.Driver); ok {
			return []databaseReadPlan{{
				ShouldQuery: true,
				SQL:         query,
				Reason:      "Listed live schemas and tables because the request asked to inspect the database structure before querying data.",
				Target:      "schema inventory",
			}}, true
		}
	}

	if len(inspection.Objects) == 0 {
		return nil, false
	}

	objects := selectDeterministicDatabaseObjects(lowerQuestion, inspection.Objects)
	if len(objects) == 0 {
		return nil, false
	}
	if len(objects) > maxDeterministicTableQueries {
		objects = objects[:maxDeterministicTableQueries]
	}

	plans := make([]databaseReadPlan, 0, len(objects)*2)
	wantsCounts := hasDeterministicCountIntent(lowerQuestion)
	wantsLatest := hasDeterministicLatestIntent(lowerQuestion)
	wantsPreview := hasDeterministicPreviewIntent(lowerQuestion)

	if wantsCounts {
		for _, object := range objects {
			qualifiedName := qualifiedDatabaseObjectName(inspection.Connection.Driver, object)
			if qualifiedName == "" {
				continue
			}
			plans = append(plans, databaseReadPlan{
				ShouldQuery: true,
				SQL:         fmt.Sprintf("SELECT COUNT(*) AS row_count FROM %s", qualifiedName),
				Reason:      fmt.Sprintf("Counted rows from %s because the request asked for database totals.", renderObjectName(object)),
				Target:      renderObjectName(object),
			})
		}
	}

	if wantsLatest {
		for _, object := range objects {
			qualifiedName := qualifiedDatabaseObjectName(inspection.Connection.Driver, object)
			if qualifiedName == "" {
				continue
			}

			previewColumns := previewColumnList(inspection.Connection.Driver, object)
			if previewColumns == "" {
				previewColumns = "*"
			}

			if orderColumn := preferredOrderColumn(inspection.Connection.Driver, object); orderColumn != "" {
				plans = append(plans, databaseReadPlan{
					ShouldQuery: true,
					SQL:         fmt.Sprintf("SELECT %s FROM %s ORDER BY %s DESC LIMIT %d", previewColumns, qualifiedName, orderColumn, defaultDatabasePreviewRows),
					Reason:      fmt.Sprintf("Previewed recent rows from %s using the most likely ordering column.", renderObjectName(object)),
					Target:      renderObjectName(object),
				})
			}
		}
	}

	if wantsPreview && !wantsLatest {
		for _, object := range objects {
			qualifiedName := qualifiedDatabaseObjectName(inspection.Connection.Driver, object)
			if qualifiedName == "" {
				continue
			}

			previewColumns := previewColumnList(inspection.Connection.Driver, object)
			if previewColumns == "" {
				previewColumns = "*"
			}

			plans = append(plans, databaseReadPlan{
				ShouldQuery: true,
				SQL:         fmt.Sprintf("SELECT %s FROM %s LIMIT %d", previewColumns, qualifiedName, defaultDatabasePreviewRows),
				Reason:      fmt.Sprintf("Previewed rows from %s because the request asked to inspect live data.", renderObjectName(object)),
				Target:      renderObjectName(object),
			})
		}
	}

	if len(plans) > 0 {
		return plans, true
	}

	return nil, false
}

func selectDeterministicDatabaseObject(lowerQuestion string, objects []dbcontext.Object) (dbcontext.Object, bool) {
	matches := selectDeterministicDatabaseObjects(lowerQuestion, objects)
	if len(matches) != 1 {
		return dbcontext.Object{}, false
	}
	return matches[0], true
}

func selectDeterministicDatabaseObjects(lowerQuestion string, objects []dbcontext.Object) []dbcontext.Object {
	if len(objects) == 0 {
		return nil
	}

	candidates := make([]dbcontext.Object, 0, len(objects))
	for _, object := range objects {
		if object.Type != "table" && object.Type != "view" {
			continue
		}
		candidates = append(candidates, object)
	}
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates
	}

	matches := make([]dbcontext.Object, 0, len(candidates))
	for _, object := range candidates {
		if databaseObjectMatchesQuestion(object, lowerQuestion) {
			matches = append(matches, object)
		}
	}
	if len(matches) > 0 {
		tableMatches := make([]dbcontext.Object, 0, len(matches))
		for _, object := range matches {
			if object.Type == "table" {
				tableMatches = append(tableMatches, object)
			}
		}
		if len(tableMatches) > 0 {
			return tableMatches
		}
		return matches
	}

	tables := make([]dbcontext.Object, 0, len(candidates))
	for _, object := range candidates {
		if object.Type == "table" {
			tables = append(tables, object)
		}
	}
	if len(tables) == 1 {
		return tables
	}

	return nil
}

func databaseObjectMatchesQuestion(object dbcontext.Object, lowerQuestion string) bool {
	qualifiedName := strings.ToLower(strings.TrimSpace(renderObjectName(object)))
	if qualifiedName != "" && strings.Contains(lowerQuestion, qualifiedName) {
		return true
	}

	name := strings.ToLower(strings.TrimSpace(object.Name))
	if name != "" && strings.Contains(lowerQuestion, name) {
		return true
	}

	for _, column := range object.Columns {
		columnName := strings.ToLower(strings.TrimSpace(column.Name))
		if columnName != "" && strings.Contains(lowerQuestion, columnName) {
			return true
		}
	}

	return false
}

func hasDeterministicPreviewIntent(lowerQuestion string) bool {
	return containsAnyPhrase(lowerQuestion,
		"show me all", "all the ", "everything in", "everything from",
		"tell me about", "what are they about", "what is it about", "what are these about",
		"what is this about", "what do they contain", "what does it contain", "describe ",
		"tell me what i have", "tell me what i have in", "query ",
		"tell me what data", "what data", "what rows", "what's in", "what is in",
		"preview", "preview rows", "show rows", "sample rows", "query some columns",
		"query some", "read some", "show me data", "list rows", "inspect rows")
}

func hasDeterministicSchemaInventoryIntent(lowerQuestion string) bool {
	if !containsAnyPhrase(lowerQuestion, "database", "schema", "schemas", "table", "tables", "inspect", "query") {
		return false
	}

	return containsAnyPhrase(lowerQuestion,
		"inspect the selected database",
		"inspect this database",
		"summarize the schemas",
		"summarize schemas",
		"main tables",
		"list tables",
		"show tables",
		"what tables",
		"what schemas",
		"schema inventory",
		"table inventory",
		"help me query it",
		"help me query this")
}

func hasDeterministicCountIntent(lowerQuestion string) bool {
	return containsAnyPhrase(lowerQuestion, "how many", "count", "number of", "total")
}

func hasDeterministicLatestIntent(lowerQuestion string) bool {
	return containsAnyPhrase(lowerQuestion, "latest ", "recent ", "newest ", "last ", "most recent")
}

func qualifiedDatabaseObjectName(driver string, object dbcontext.Object) string {
	quotedName := quoteDatabaseIdentifier(driver, object.Name)
	if quotedName == "" {
		return ""
	}
	if strings.TrimSpace(object.Schema) == "" {
		return quotedName
	}
	return quoteDatabaseIdentifier(driver, object.Schema) + "." + quotedName
}

func previewColumnList(driver string, object dbcontext.Object) string {
	if len(object.Columns) == 0 {
		return "*"
	}

	columns := make([]string, 0, len(object.Columns))
	for _, column := range object.Columns {
		quoted := quoteDatabaseIdentifier(driver, column.Name)
		if quoted == "" {
			continue
		}
		columns = append(columns, quoted)
	}
	if len(columns) == 0 {
		return "*"
	}
	return strings.Join(columns, ", ")
}

func preferredOrderColumn(driver string, object dbcontext.Object) string {
	for _, column := range object.Columns {
		name := strings.ToLower(strings.TrimSpace(column.Name))
		if name == "created_at" || name == "updated_at" || name == "checked_at" || name == "timestamp" || name == "time" || name == "date" {
			return quoteDatabaseIdentifier(driver, column.Name)
		}
		if strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_time") || strings.HasSuffix(name, "_date") {
			return quoteDatabaseIdentifier(driver, column.Name)
		}
		typeName := strings.ToLower(strings.TrimSpace(column.Type))
		if strings.Contains(typeName, "timestamp") || strings.Contains(typeName, "date") || strings.Contains(typeName, "time") {
			return quoteDatabaseIdentifier(driver, column.Name)
		}
	}

	for _, column := range object.Columns {
		name := strings.ToLower(strings.TrimSpace(column.Name))
		if name == "id" || strings.HasSuffix(name, "_id") {
			return quoteDatabaseIdentifier(driver, column.Name)
		}
	}

	return ""
}

func quoteDatabaseIdentifier(driver string, identifier string) string {
	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" {
		return ""
	}

	if driver == "mysql" {
		return "`" + strings.ReplaceAll(trimmed, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(trimmed, `"`, `""`) + `"`
}

func renderObjectName(object dbcontext.Object) string {
	if strings.TrimSpace(object.Schema) == "" {
		return object.Name
	}
	return object.Schema + "." + object.Name
}

func deterministicSchemaInventoryQuery(driver string) (string, bool) {
	switch strings.TrimSpace(driver) {
	case "postgres":
		return strings.Join([]string{
			"SELECT table_schema, table_name, table_type",
			"FROM information_schema.tables",
			"WHERE table_schema NOT IN ('pg_catalog', 'information_schema')",
			"ORDER BY CASE WHEN table_schema = 'public' THEN 0 ELSE 1 END, table_schema, table_name",
			fmt.Sprintf("LIMIT %d", defaultDatabasePreviewRows*3),
		}, " "), true
	case "mysql":
		return strings.Join([]string{
			"SELECT table_schema, table_name, table_type",
			"FROM information_schema.tables",
			"WHERE table_schema = DATABASE()",
			"ORDER BY table_name",
			fmt.Sprintf("LIMIT %d", defaultDatabasePreviewRows*3),
		}, " "), true
	case "sqlite":
		return strings.Join([]string{
			"SELECT 'main' AS table_schema, name AS table_name, type AS table_type",
			"FROM sqlite_master",
			"WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'",
			"ORDER BY name",
			fmt.Sprintf("LIMIT %d", defaultDatabasePreviewRows*3),
		}, " "), true
	default:
		return "", false
	}
}

func extractDirectReadOnlySQL(question string) string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return ""
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "select ") || strings.HasPrefix(lower, "with ") {
		return trimmed
	}

	lines := strings.Split(trimmed, "\n")
	inFence := false
	fenced := make([]string, 0, len(lines))
	for _, line := range lines {
		lineTrimmed := strings.TrimSpace(line)
		if strings.HasPrefix(lineTrimmed, "```") {
			if inFence {
				candidate := strings.TrimSpace(strings.Join(fenced, "\n"))
				lowerCandidate := strings.ToLower(candidate)
				if strings.HasPrefix(lowerCandidate, "select ") || strings.HasPrefix(lowerCandidate, "with ") {
					return candidate
				}
				break
			}
			inFence = true
			continue
		}
		if inFence {
			fenced = append(fenced, line)
		}
	}

	return ""
}

func planDatabaseReadQuery(ctx context.Context, aiClient *ai.Client, question string, inspection dbcontext.Inspection) (databaseReadPlan, error) {
	return planDatabaseReadQueryWithContext(ctx, aiClient, question, inspection, "")
}

func formatInspectionForQueryPlanning(inspection dbcontext.Inspection) string {
	b := &strings.Builder{}
	if inspection.Version != "" {
		b.WriteString(fmt.Sprintf("Engine Version: %s\n", inspection.Version))
	}
	if inspection.CurrentDatabase != "" {
		b.WriteString(fmt.Sprintf("Current Database: %s\n", inspection.CurrentDatabase))
	}
	if inspection.SchemaCount > 0 || inspection.TableCount > 0 || inspection.ViewCount > 0 {
		b.WriteString(fmt.Sprintf("Schema Summary: %d schemas, %d tables, %d views\n", inspection.SchemaCount, inspection.TableCount, inspection.ViewCount))
	}
	if len(inspection.TopSchemas) > 0 {
		b.WriteString("Top Schemas:\n")
		for _, schema := range inspection.TopSchemas {
			b.WriteString(fmt.Sprintf("- %s: %d tables, %d views\n", schema.Schema, schema.TableCount, schema.ViewCount))
		}
	}
	if len(inspection.LargestTables) > 0 {
		b.WriteString("Largest Tables:\n")
		for _, table := range inspection.LargestTables {
			name := table.Name
			if table.Schema != "" {
				name = table.Schema + "." + table.Name
			}
			b.WriteString(fmt.Sprintf("- %s [%s] size=%d bytes rows≈%d\n", name, table.Type, table.SizeBytes, table.RowEstimate))
		}
	}
	if len(inspection.Objects) == 0 {
		b.WriteString("Objects: none discovered\n")
		return b.String()
	}

	b.WriteString("Objects:\n")
	for _, object := range inspection.Objects {
		name := object.Name
		if object.Schema != "" {
			name = object.Schema + "." + object.Name
		}
		b.WriteString(fmt.Sprintf("- %s [%s]\n", name, object.Type))
		if len(object.Columns) == 0 {
			continue
		}
		parts := make([]string, 0, len(object.Columns))
		for _, column := range object.Columns {
			nullability := "not null"
			if column.Nullable {
				nullability = "nullable"
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", column.Name, column.Type, nullability))
		}
		b.WriteString("  Columns: ")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n")
	}

	return b.String()
}

func formatDatabaseQueryResult(result dbcontext.QueryResult, reason string) string {
	b := &strings.Builder{}
	b.WriteString(fmt.Sprintf("Connection: %s [%s] %s\n", result.Connection.Name, result.Connection.Kind(), result.Connection.Target()))
	if reason != "" {
		b.WriteString(fmt.Sprintf("Reason: %s\n", reason))
	}
	b.WriteString(fmt.Sprintf("SQL: %s\n", result.Query))
	if len(result.Columns) > 0 {
		b.WriteString(fmt.Sprintf("Columns: %s\n", strings.Join(result.Columns, ", ")))
	}
	b.WriteString(fmt.Sprintf("Rows Returned: %d\n", len(result.Rows)))
	if len(result.Rows) == 0 {
		b.WriteString("Result: no rows\n")
		return b.String()
	}

	limit := len(result.Rows)
	if limit > maxDatabaseQueryPromptRows {
		limit = maxDatabaseQueryPromptRows
	}

	b.WriteString("Rows:\n")
	for i := 0; i < limit; i++ {
		parts := make([]string, 0, len(result.Columns))
		for _, column := range result.Columns {
			parts = append(parts, fmt.Sprintf("%s=%s", column, result.Rows[i][column]))
		}
		b.WriteString("- ")
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString("\n")
	}
	if len(result.Rows) > limit {
		b.WriteString(fmt.Sprintf("- ... %d additional rows omitted\n", len(result.Rows)-limit))
	}
	if result.Truncated {
		b.WriteString("- Additional rows were truncated at the read safety limit\n")
	}

	return b.String()
}

func collectCICDAgentContext(ctx context.Context, question string, debug bool) ([]domainContextSection, []string) {
	sections := make([]domainContextSection, 0, 8)
	warnings := make([]string, 0, 8)

	if shouldQueryDomainProvider(question, "github") {
		githubQuestion := buildGitHubCICDContextQuestion(question)
		githubToken := viper.GetString("github.token")
		trackedRepos := configuredGitHubReposForCICDAgent()
		if len(trackedRepos) == 0 {
			githubClient := ghclient.NewClient(githubToken, viper.GetString("github.owner"), viper.GetString("github.repo"))
			githubInfo, err := githubClient.GetRelevantContext(ctx, githubQuestion)
			if err != nil {
				warnings = appendDomainWarning(warnings, "GitHub CI/CD inventory", err)
			} else if hasGitHubActionsEvidence(githubInfo) {
				sections = appendDomainSection(sections, "GitHub Actions", githubInfo)
			}
		} else {
			foundGitHubEvidence := false
			for _, fullName := range trackedRepos {
				owner, repo, ok := splitGitHubTrackedRepo(fullName)
				if !ok {
					continue
				}
				githubClient := ghclient.NewClient(githubToken, owner, repo)
				githubInfo, err := githubClient.GetRelevantContext(ctx, githubQuestion)
				if err != nil {
					warnings = appendDomainWarning(warnings, fmt.Sprintf("GitHub CI/CD inventory (%s)", fullName), err)
					continue
				}
				if !hasGitHubActionsEvidence(githubInfo) {
					continue
				}
				sections = appendDomainSection(sections, fmt.Sprintf("GitHub Actions (%s)", fullName), githubInfo)
				foundGitHubEvidence = true
			}
			if !foundGitHubEvidence {
				warnings = append(warnings, "GitHub CI/CD inventory: no GitHub Actions workflows or runners found in tracked repositories")
			}
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

	if shouldQueryDomainProvider(question, "azure") {
		azureClient := azure.NewClientWithOptionalSubscription(strings.TrimSpace(azure.ResolveSubscriptionID()), debug)
		azureInfo, azureErr := azureClient.GetRelevantContext(ctx, "azure devops pipelines runs builds releases repositories")
		if azureErr != nil {
			warnings = appendDomainWarning(warnings, "Azure DevOps inventory", azureErr)
		} else {
			sections = appendDomainSection(sections, "Azure DevOps", azureInfo)
		}
	}
	if shouldQueryDomainProvider(question, "hetzner") && hasHetznerDomainAccess() {
		warnings = appendUnsupportedFeature(warnings, "Hetzner", "build or deployment inventory")
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
- Azure SQL, Azure Database for PostgreSQL/MySQL, Cosmos DB, Azure Cache for Redis
- DigitalOcean managed databases
- Cloudflare D1

Rules:
- Separate observed facts from recommendations.
- This agent is strictly read-only. Never propose or imply running write or migration commands during inspection.
- For database estate or inventory questions about multiple databases or "my dbs/databases", prioritize configured connection status and cloud-managed database inventory over any single selected database connection.
- When live SQL read results are present, use them as the primary answer and identify the connection they came from.
- When a Database Query Retry Outcome section is present, report the last observed failure and explicitly note that the query self-heal budget was exhausted after 3 attempts.
- If multiple connections returned results, report them per connection. Only provide an aggregate if it is obviously additive.
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
- Azure DevOps pipelines, runs, and repositories
- Cloudflare deployments and Pages projects
- DigitalOcean App Platform deployments

Rules:
- Separate observed facts from recommendations.
- When GitHub Actions evidence spans multiple repositories, report findings per repository instead of collapsing them together.
- Prefer detailed run, job, failed-step, annotation, artifact, and log evidence over generic workflow summaries when it is available.
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

func buildSoftwareBlocksAgentPrompt(question string) string {
	b := &strings.Builder{}
	b.WriteString(`You are Clanker's software-blocks agent.

Your job is to classify a connected infrastructure system into software architecture blocks, not just cloud provider resource types.

Return JSON only. Do not wrap the output in markdown. Do not include prose before or after the JSON.

Required JSON schema:
{
	"systemType": "short human-readable type for this system",
	"summary": "one sentence summary of what the system appears to be",
	"blocks": [
		{
			"id": "stable-slug",
			"name": "Block name from the taxonomy when possible",
			"category": "core|data|infrastructure|reliability|security|workflow|ai",
			"description": "short purpose in this system",
			"resourceIds": ["exact resource ids from input"],
			"confidence": 0.0,
			"evidence": ["short observed reason"]
		}
	],
	"resources": [
		{
			"resourceId": "exact resource id from input",
			"blockId": "stable-slug matching a block id",
			"block": "Block name",
			"category": "core|data|infrastructure|reliability|security|workflow|ai",
			"confidence": 0.0,
			"reason": "short observed reason"
		}
	]
}

Block taxonomy:
- Core application: UI / Frontend, API, Backend service, Database, Object storage, Cache, Queue, Worker, Scheduler / Cron, Auth, Search, Realtime channel, Notifications, Payments / Billing, Admin / Backoffice, Feature flags, Config / Secrets
- Data and integration: Schema / Models, Migrations, Validation, ETL / Pipelines, Webhooks, Connectors, Embeddings / Vector store, File processing, Analytics events
- Infrastructure: Compute, Container, Load balancer, CDN, DNS, Network / VPC, Firewall / WAF, Service discovery, Ingress / Gateway, Autoscaling, Infrastructure as Code, Environment
- Reliability: Logs, Metrics, Traces, Alerts, Health checks, Retries, Rate limits, Circuit breakers, Backups, Disaster recovery, Audit logs
- Security: Identity, Authorization, Roles / Permissions, API keys, Secrets management, Encryption, Policy engine, Compliance controls, Security scanning, Tenant isolation
- Developer workflow: Repo, CI/CD pipeline, Tests, Preview environments, Build system, Package registry, Rollback, Documentation, Runbooks
- AI-native: Prompt / Instruction, Tool call, Memory, RAG knowledge base, Agent, Eval, Guardrail, Human approval gate, Simulation / Dry run

Rules:
- Use exact resource ids from the input.
- Every input resource should appear exactly once in resources.
- Prefer specific software block names from the taxonomy.
- Classify by observed role and connectivity, not by label alone.
- If a resource is only infrastructure support, classify it under Infrastructure or Security rather than inventing an app role.
- If evidence is ambiguous, choose the safest block and lower confidence.
- Do not invent resources, ids, providers, connections, or states.

Input:
`)
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n")
	return b.String()
}

func buildDataFlowAgentPrompt(question string) string {
	b := &strings.Builder{}
	b.WriteString(`You are Clanker's data_flow agent.

Your job is to map directional runtime data flow from the public Internet into and through connected infrastructure resources.

Return JSON only. Do not wrap the output in markdown. Do not include prose before or after the JSON.

Required JSON schema:
{
	"summary": "one sentence summary of the likely data flow",
	"flows": [
		{
			"id": "stable-flow-slug",
			"sourceResourceId": "exact source resource id from input, or internet for public client/browser traffic",
			"targetResourceId": "exact target resource id from input",
			"label": "Data flow",
			"data": "short payload or traffic description shown on the arrow",
			"protocol": "HTTP|HTTPS|SQL|TCP|Events|Queue|Object|Unknown",
			"confidence": 0.0,
			"evidence": ["short observed reason"]
		}
	],
	"resourceLabels": [
		{
			"resourceId": "exact resource id from input",
			"label": "short role in the flow"
		}
	]
}

Rules:
- Use exact resource ids from the input.
- Only create flows between resources that exist in the input, except sourceResourceId may be "internet" for public Internet/client traffic.
- If a system has a public API gateway, load balancer, CDN, DNS, ingress, public endpoint, browser/client entrypoint, or serverless HTTP function, include a first flow from sourceResourceId "internet" into that entry resource.
- Prefer observed links, typed connections, names, endpoints, and software-block classifications as evidence.
- A flow must represent data movement or request traffic, not just ownership, placement, or security attachment.
- Set label to "Data flow" for every returned flow.
- Put the concrete arrow text in the data field, for example "browser HTTPS request", "API JSON payload", "SQL query", or "object upload".
- Use protocol "Unknown" when the protocol cannot be inferred safely.
- If direction is ambiguous, choose the most likely runtime direction and lower confidence.
- Do not invent resources, ids, providers, connections, credentials, or states.

Input:
`)
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n")
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

// appendUnsupportedFeature records that a provider lacks coverage for a
// specific area. Distinct from appendDomainWarning so the agent prompt can
// frame these as documented limitations rather than transient errors.
func appendUnsupportedFeature(warnings []string, provider, area string) []string {
	return append(warnings, fmt.Sprintf("%s %s: not yet supported by Clanker (tracked feature gap)", provider, area))
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
	if extractDirectReadOnlySQL(question) != "" {
		return true
	}
	if isDatabaseInfrastructureInventoryQuestion(question) {
		return true
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

func shouldRouteToDatabaseAgentWithContext(question string, dbConnection string) bool {
	if shouldRouteToDatabaseAgent(question) {
		return true
	}
	if !hasConfiguredDatabaseConnection(dbConnection) {
		return false
	}
	if hasConflictingDatabaseReadIntent(question) {
		return false
	}
	return shouldAttemptLiveDatabaseReads(question)
}

func shouldAttemptLiveDatabaseReads(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if isDatabaseInfrastructureInventoryQuestion(lower) {
		return false
	}
	if extractDirectReadOnlySQL(question) != "" {
		return true
	}
	if containsAnyPhrase(lower, "schema", "schemas", "migration", "migrations", "foreign key", "primary key", "index", "indexes", "connection string", "database connection", "db connection") &&
		!containsAnyPhrase(lower, "how many", "count", "number of", "total", "show me", "list ", "find ", "search ", "latest ", "recent ", "newest ", "oldest ", "top ", "sample ", "first ", "last ", "do i have any", "are there any") {
		return false
	}
	return containsAnyPhrase(lower,
		"how many", "count", "number of", "total", "show me", "list ", "find ", "search ",
		"latest ", "recent ", "newest ", "oldest ", "top ", "sample ", "first ", "last ",
		"best ", "highest ", "most ", "largest ", "lowest ", "ranking", "rank ", "leaderboard", "revenue",
		"do i have any", "are there any", "which ", "who ",
		"show me all", "all the ", "everything in", "everything from",
		"tell me what i have", "tell me what i have in", "query ",
		"tell me what data", "what data", "what rows", "what's in", "what is in",
		"preview", "preview rows", "show rows", "sample rows", "query some columns",
		"query some", "read some", "show me data", "list rows", "inspect rows")
}

func isDatabaseInfrastructureInventoryQuestion(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if extractDirectReadOnlySQL(question) != "" {
		return false
	}
	if containsAnyPhrase(lower,
		"schema", "schemas", "table", "tables", "column", "columns", "row", "rows",
		"foreign key", "primary key", "index", "indexes", "sql", "select ", "with ",
		"query ", "join ", "group by", "order by", "customer", "customers", "order", "orders") {
		return false
	}
	if containsAnyPhrase(lower,
		"how many databases", "how many dbs", "how are my dbs", "how are my databases",
		"how many database", "what database do i have", "which database do i have",
		"status of my dbs", "status of my databases", "health of my dbs", "health of my databases",
		"show me my databases", "show my databases", "show me my dbs", "show my dbs",
		"list my databases", "list databases", "list my dbs", "list dbs",
		"what databases do i have", "which databases do i have", "database inventory", "db inventory",
		"my databases", "my dbs", "managed databases", "database estate", "db estate") {
		return true
	}
	return (containsAnyPhrase(lower, "databases", "dbs") || containsAnyPhrase(lower, "my database", "my databases", "my dbs")) &&
		containsAnyPhrase(lower, "how many", "how are", "status", "health", "show", "list ", "what ", "which ")
}

func hasConfiguredDatabaseConnection(dbConnection string) bool {
	if strings.TrimSpace(dbConnection) != "" {
		_, err := dbcontext.ResolveConnection(dbConnection)
		return err == nil
	}
	connections, _, err := dbcontext.ListConnections()
	return err == nil && len(connections) > 0
}

func hasConflictingDatabaseReadIntent(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if containsAnyPhrase(lower, "github actions", "workflow", "workflow run", "codebuild", "codepipeline", "cloud build", "cloud deploy", "pipeline", "pipelines", "runner", "runners") &&
		!containsAnyPhrase(lower, "database", "schema", "sql", "nosql", "table", "tables", "column", "columns", "migration", "postgres", "mysql", "sqlite", "supabase", "neon", "dynamodb", "firestore", "spanner", "bigtable", "cosmos", "d1", "redis", "mongo") {
		return true
	}
	if containsAnyPhrase(lower, "pod", "pods", "deployment", "deployments", "statefulset", "daemonset", "helm", "kubectl", "namespace", "service", "services", "cluster", "node", "nodes") &&
		!containsAnyPhrase(lower, "database", "schema", "sql", "nosql", "table", "tables", "column", "columns", "migration", "migrations", "connection", "query") {
		return true
	}
	if containsAnyPhrase(lower, "iam", "role", "policy", "access key", "github", "gitlab") &&
		!containsAnyPhrase(lower, "database", "sql", "table", "tables", "postgres", "mysql", "sqlite", "supabase", "neon") {
		return true
	}
	return false
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
