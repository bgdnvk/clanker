package cmd

import "testing"

// Routing regression suite — every case here failed pre-fix on master and
// is locked in here to prevent recurrence. The CLI's
// determineRoutingDecisionDetailsWithContext powers `clanker ask
// --route-only`, which clanker-cloud's backend calls to get the CLI's
// routing opinion. Mis-routes here turn into mis-routes in the desktop
// app, so the contract is load-bearing.

// shouldRouteToDatabaseAgent — bare "schema"/"schemas"/"index"/"indexes"
// used to short-circuit non-database questions to the database agent.
// Each of these cases ran the GraphQL/JSON/zod/search-index path and
// got the wrong answer before the cmd/domain_agents.go cleanup.
func TestShouldRouteToDatabaseAgent_NoFalsePositivesForSchemaOrIndex(t *testing.T) {
	for _, q := range []string{
		"the graphql schema is broken",
		"validate the json schema for this payload",
		"review the zod schema",
		"the search index is stale",
		"reindex the docs site",
		"build an inverted index for search",
	} {
		if shouldRouteToDatabaseAgent(q) {
			t.Errorf("query %q should NOT route to database agent", q)
		}
	}
}

// Real database questions still match — the matchers we kept (engine
// names, sql/migration/connection phrases, table+engine combos) are
// sufficient.
func TestShouldRouteToDatabaseAgent_RealDatabaseQueriesStillMatch(t *testing.T) {
	for _, q := range []string{
		"show me my postgres tables",
		"where is the redis cache",
		"list databases in production",
		"what are my mysql users",
		"how do i run a migration",
		"my supabase connection is failing",
		"foreign key constraint failed",
	} {
		if !shouldRouteToDatabaseAgent(q) {
			t.Errorf("query %q SHOULD route to database agent", q)
		}
	}
}

// shouldIncludeDatabaseContextWithContext — same family of bare-token
// false-positives as above, but for the DB-context-injection gate.
// Taint here biases the model toward DB phrasing even when the agent
// itself routes elsewhere.
func TestShouldIncludeDatabaseContextWithContext_NoFalsePositives(t *testing.T) {
	for _, q := range []string{
		"the graphql schema is broken",
		"validate the json schema",
		"the search index is stale",
		"reindex the docs",
		"i have a column problem in this table tennis matchup",
		"list ec2 instances",
		"my pod is crashlooping",
	} {
		if shouldIncludeDatabaseContextWithContext(q, "") {
			t.Errorf("query %q should NOT trigger DB context inclusion", q)
		}
	}
}

func TestShouldIncludeDatabaseContextWithContext_RealDBStillMatches(t *testing.T) {
	for _, q := range []string{
		"show me my postgres tables",
		"my database connection is timing out",
		"how do i run a migration",
		"foreign key constraint failed in mysql",
		"connect to supabase",
	} {
		if !shouldIncludeDatabaseContextWithContext(q, "") {
			t.Errorf("query %q SHOULD trigger DB context inclusion", q)
		}
	}
}

func TestShouldRouteToObservabilityAgent(t *testing.T) {
	for _, q := range []string{
		"show me recent error logs for prod api",
		"trace 500 errors in cloud run",
		"show cloudwatch alarms and warning logs",
		"what sentry issues are active",
		"find crashloop warnings in my pods",
	} {
		if !shouldRouteToObservabilityAgent(q) {
			t.Errorf("query %q SHOULD route to observability agent", q)
		}
	}
}

func TestShouldRouteToObservabilityAgent_DoesNotStealCICDOrDB(t *testing.T) {
	for _, q := range []string{
		"show me failing github actions workflows",
		"what is the deployment status for github actions",
		"my database connection is failing",
		"show me postgres tables",
	} {
		if shouldRouteToObservabilityAgent(q) {
			t.Errorf("query %q should NOT route to observability agent", q)
		}
	}
}

// End-to-end via determineRoutingDecisionDetailsWithContext (the path
// `clanker ask --route-only` exposes to clanker-cloud's backend).
// Each case below was wrong on master pre-fix.
func TestDetermineRoutingDecision_PostFixRegressions(t *testing.T) {
	cases := []struct {
		query string
		want  string
		note  string
	}{
		// Schema/index false-positives (cmd/domain_agents.go phrase list cleanup)
		{"the graphql schema is broken", "cli", "no bare-schema fallback"},
		{"validate the json schema for this payload", "cli", "no bare-schema fallback"},
		{"the search index is stale", "cli", "no bare-index fallback"},
		{"reindex the docs", "cli", "no bare-index fallback"},

		// K8s/database overlap (cmd/ask.go::k8sResources cleanup — postgres/mysql/redis/mongodb removed)
		{"create a postgres table", "maker", "postgres not in k8sResources anymore"},
		{"spin up a postgres database", "maker", "postgres not in k8sResources anymore"},

		// CICD-vs-deployment overlap (cmd/ask.go early CICD bypass added)
		{"setup github actions for deployment", "agent-cicd", "github-actions wins over deployment+action"},
		{"deploy via github actions", "agent-cicd", "explicit cicd context"},
		{"show me failing github actions workflows", "agent-cicd", "workflow status stays with cicd"},

		// Observability intent
		{"show me recent error logs for prod api", "agent-observability", "runtime error logs"},
		{"trace 500 errors in cloud run", "agent-observability", "trace/error request"},
		{"show cloudwatch alarms and warning logs", "agent-observability", "cloudwatch alarms/logs"},

		// Real cases that must still route correctly
		{"how many lambda do i have", "cli", "no action keyword + non-DB"},
		{"show me my postgres tables", "agent-database", "engine + table combo"},
		{"list databases in production", "agent-database", "explicit databases keyword"},
		{"create a k8s deployment", "k8s-maker", "deployment + action with no CICD signal"},
		{"create a lambda", "maker", "lambda + action"},
	}
	for _, tc := range cases {
		decision := determineRoutingDecisionDetailsWithContext(tc.query, "")
		if decision.Agent != tc.want {
			t.Errorf("query %q: want agent=%q (%s), got %q (reason=%q)",
				tc.query, tc.want, tc.note, decision.Agent, decision.Reason)
		}
	}
}

func TestDetermineRoutingDecision_IgnoresAppendedContextForRouteOnly(t *testing.T) {
	const appendedContext = `Current infrastructure context (compact)
INFRA_SUMMARY total=3 topTypes=lambda:1,database:1
INFRA_INDEX id|type|region|name
lambdatron-db-connector|lambda|us-east-1|lambdatron-db-connector

DATABASE_ESTATE_RESOURCES
managed database inventory present`

	decision := determineRoutingDecisionDetailsWithContext("how many lambdas do i have?\n\n"+appendedContext, "")
	if decision.Agent != "cli" {
		t.Fatalf("lambda inventory question with appended context should route to cli, got %q reason=%q", decision.Agent, decision.Reason)
	}
}
