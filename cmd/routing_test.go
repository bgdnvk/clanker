package cmd

import "testing"

// Routing-decision regression suite. The CLI re-runs its own routing on
// every `clanker ask` call (see ask.go:936 and the route-only path used
// by clanker-cloud's backend), so the same bare-token false-positives
// that bit the backend router will bite the CLI too. These tests pin the
// fix from clanker-cloud #409 in the CLI: bare "schema"/"index"/"db"
// tokens must NOT force database routing for non-database questions.

func TestShouldRouteToDatabaseAgent_NoFalsePositives(t *testing.T) {
	cases := []string{
		"fix my deploy db backup script",
		"are there any new dbs in staging",
		"the graphql schema is broken",
		"validate the json schema for this payload",
		"my k8s namespace is called db, list pods",
		"the search index is stale",
		"reindex the page index",
		"what's my aws bill this month",
		"list ec2 instances",
		"why is my lambda timing out",
		"check cloudwatch logs",
		"my pod is crashlooping",
	}
	for _, q := range cases {
		if shouldRouteToDatabaseAgent(q) {
			t.Errorf("query %q should NOT route to database agent", q)
		}
	}
}

func TestShouldRouteToDatabaseAgent_TrueMatches(t *testing.T) {
	cases := []string{
		"show me my postgres tables",
		"where is the redis cache",
		"list databases in production",
		"what are my mysql users",
		"check the dynamodb tables i have",
		"my supabase connection is failing",
	}
	for _, q := range cases {
		if !shouldRouteToDatabaseAgent(q) {
			t.Errorf("query %q SHOULD route to database agent", q)
		}
	}
}

// shouldIncludeDatabaseContextWithContext is the other gate that injects
// DB context into the Clanker prompt. Even if the agent isn't database,
// a true here taints the prompt with database information and biases the
// model's answer toward DB phrasing. The same bare-token cleanup applies.
func TestShouldIncludeDatabaseContextWithContext_NoFalsePositives(t *testing.T) {
	cases := []string{
		"the graphql schema is broken",
		"validate the json schema",
		"the search index is stale",
		"i have a column problem in this table tennis matchup",
		"reindex the docs site",
		"list ec2 instances",
		"my pod is crashlooping",
	}
	for _, q := range cases {
		if shouldIncludeDatabaseContextWithContext(q, "") {
			t.Errorf("query %q should NOT trigger DB context inclusion", q)
		}
	}
}

func TestShouldIncludeDatabaseContextWithContext_TrueMatches(t *testing.T) {
	cases := []string{
		"show me my postgres tables",
		"my database connection is timing out",
		"how do i run a migration",
		"foreign key constraint failed in mysql",
		"connect to supabase",
	}
	for _, q := range cases {
		if !shouldIncludeDatabaseContextWithContext(q, "") {
			t.Errorf("query %q SHOULD trigger DB context inclusion", q)
		}
	}
}

// End-to-end check: the routing decision function used by --route-only
// and by the in-process `clanker ask` dispatch must agree with our gates.
// If this drifts, the CLI will say "agent-cli" on the routing probe but
// then internally hijack to database during execution (or vice versa).
func TestDetermineRoutingDecision_NoDatabaseFallthroughs(t *testing.T) {
	cases := []string{
		"fix my deploy db backup script",
		"the graphql schema is broken",
		"validate the json schema",
		"what's my aws bill",
		"list ec2 instances",
		"my pod is crashlooping",
		"the search index is stale",
	}
	for _, q := range cases {
		decision := determineRoutingDecisionDetailsWithContext(q, "")
		if decision.Agent == "agent-database" {
			t.Errorf("query %q routed to %s (reason=%q) — must not be database", q, decision.Agent, decision.Reason)
		}
	}
}

func TestDetermineRoutingDecision_DatabaseMatchesStillRoute(t *testing.T) {
	cases := []string{
		"show me my postgres tables",
		"list databases in production",
		"what are my mysql users",
	}
	for _, q := range cases {
		decision := determineRoutingDecisionDetailsWithContext(q, "")
		if decision.Agent != "agent-database" {
			t.Errorf("query %q routed to %s, want agent-database (reason=%q)", q, decision.Agent, decision.Reason)
		}
	}
}
