package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/dbcontext"
)

const (
	maxDatabaseQueryHealAttempts = 3
	runtimeDBChatHistoryEnv      = "CLANKER_RUNTIME_DB_CHAT_HISTORY"
)

type databaseQueryAttempt struct {
	Attempt int
	SQL     string
	Reason  string
	Error   string
}

type databaseQuerySubAgent struct {
	debug       bool
	chatHistory string
	aiClient    *ai.Client
}

func newDatabaseQuerySubAgent(debug bool) *databaseQuerySubAgent {
	return &databaseQuerySubAgent{
		debug:       debug,
		chatHistory: strings.TrimSpace(os.Getenv(runtimeDBChatHistoryEnv)),
	}
}

func (s *databaseQuerySubAgent) collectQueryResults(ctx context.Context, question string, dbConnection string) ([]domainContextSection, []string) {
	if !shouldAttemptLiveDatabaseReads(question) {
		return nil, nil
	}

	connections, _, err := dbcontext.ListConnections()
	if err != nil {
		return nil, []string{fmt.Sprintf("Live database query planning: %v", err)}
	}
	if len(connections) == 0 {
		return nil, nil
	}

	targetConnections, err := selectDatabaseQueryConnections(connections, question, dbConnection)
	if err != nil {
		return nil, []string{fmt.Sprintf("Live database query planning: %v", err)}
	}
	if len(targetConnections) == 0 {
		return nil, nil
	}

	directSQL := extractDirectReadOnlySQL(question)
	sections := make([]domainContextSection, 0, len(targetConnections))
	warnings := make([]string, 0, len(targetConnections))
	attemptedAny := false

	for _, connection := range targetConnections {
		inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		inspection, inspectErr := dbcontext.Inspect(inspectCtx, connection.Name)
		cancel()
		if inspectErr != nil {
			warnings = append(warnings, fmt.Sprintf("Live query inspection (%s): %v", connection.Name, inspectErr))
			continue
		}

		plannedQueries := make([]databaseReadPlan, 0, maxDeterministicTableQueries)
		switch {
		case directSQL != "":
			plannedQueries = append(plannedQueries, databaseReadPlan{
				ShouldQuery: true,
				SQL:         directSQL,
				Reason:      "Executed the user's explicit read-only SQL against this connection.",
			})
		case len(inspection.Objects) > 0:
			if deterministicPlans, ok := planDeterministicDatabaseReadQueries(question, inspection); ok && len(deterministicPlans) > 0 {
				plannedQueries = append(plannedQueries, deterministicPlans...)
			}
		}

		if len(plannedQueries) == 0 {
			plannedQueries = append(plannedQueries, databaseReadPlan{ShouldQuery: true})
		}

		for _, plan := range plannedQueries {
			attemptSections, attemptWarnings, _, tried := s.executePlanWithSelfHeal(ctx, question, connection, inspection, plan)
			if tried {
				attemptedAny = true
			}
			sections = append(sections, attemptSections...)
			warnings = append(warnings, attemptWarnings...)
		}
	}

	if !attemptedAny {
		return nil, []string{fmt.Sprintf("No safe live SQL read query matched the available schemas for: %s", strings.Join(connectionNames(targetConnections), ", "))}
	}

	return sections, warnings
}

func (s *databaseQuerySubAgent) executePlanWithSelfHeal(ctx context.Context, question string, connection dbcontext.Connection, inspection dbcontext.Inspection, seedPlan databaseReadPlan) ([]domainContextSection, []string, bool, bool) {
	attempts := make([]databaseQueryAttempt, 0, maxDatabaseQueryHealAttempts)
	warnings := make([]string, 0, 1)
	currentPlan := seedPlan
	tried := false

	for attempt := 1; attempt <= maxDatabaseQueryHealAttempts; attempt++ {
		planCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		plannedQuery, planErr := s.nextPlan(planCtx, question, inspection, currentPlan, attempts, attempt)
		cancel()
		if planErr != nil {
			tried = true
			attempts = append(attempts, databaseQueryAttempt{
				Attempt: attempt,
				Reason:  "query planning failed",
				Error:   planErr.Error(),
			})
			currentPlan = databaseReadPlan{}
			continue
		}

		currentPlan = plannedQuery
		plannedSQL := strings.TrimSpace(plannedQuery.SQL)
		if !plannedQuery.ShouldQuery || plannedSQL == "" {
			tried = true
			attempts = append(attempts, databaseQueryAttempt{
				Attempt: attempt,
				Reason:  strings.TrimSpace(plannedQuery.Reason),
				Error:   "planner did not produce a safe read-only SQL query",
			})
			currentPlan = databaseReadPlan{}
			continue
		}

		tried = true
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, queryErr := dbcontext.ExecuteReadQueryOnConnection(queryCtx, connection, plannedSQL)
		cancel()
		if queryErr == nil {
			reason := strings.TrimSpace(plannedQuery.Reason)
			if attempt > 1 {
				if reason == "" {
					reason = fmt.Sprintf("Repaired the live read query after reviewing prior failures on attempt %d/%d.", attempt, maxDatabaseQueryHealAttempts)
				} else {
					reason = fmt.Sprintf("%s Self-healed on attempt %d/%d.", reason, attempt, maxDatabaseQueryHealAttempts)
				}
				warnings = append(warnings, fmt.Sprintf("Database query subagent (%s) self-healed the SQL on attempt %d/%d", connection.Name, attempt, maxDatabaseQueryHealAttempts))
			}

			sectionTitle := fmt.Sprintf("Live Read Query Result (%s)", connection.Name)
			if strings.TrimSpace(plannedQuery.Target) != "" {
				sectionTitle = fmt.Sprintf("Live Read Query Result (%s: %s)", connection.Name, strings.TrimSpace(plannedQuery.Target))
			}

			sections := make([]domainContextSection, 0, 1)
			sections = appendDomainSection(sections, sectionTitle, formatDatabaseQueryResult(result, reason))
			return sections, warnings, true, tried
		}

		attempts = append(attempts, databaseQueryAttempt{
			Attempt: attempt,
			SQL:     plannedSQL,
			Reason:  strings.TrimSpace(plannedQuery.Reason),
			Error:   queryErr.Error(),
		})
		currentPlan = databaseReadPlan{}
	}

	sections := make([]domainContextSection, 0, 1)
	sections = appendDomainSection(sections, fmt.Sprintf("Database Query Retry Outcome (%s)", connection.Name), formatDatabaseRetryOutcome(connection, attempts, s.chatHistory))
	warnings = append(warnings, fmt.Sprintf("Database query subagent (%s) exhausted %d attempts; returning the last observed failure", connection.Name, maxDatabaseQueryHealAttempts))
	return sections, warnings, false, tried
}

func (s *databaseQuerySubAgent) nextPlan(ctx context.Context, question string, inspection dbcontext.Inspection, seedPlan databaseReadPlan, attempts []databaseQueryAttempt, attempt int) (databaseReadPlan, error) {
	if attempt == 1 && strings.TrimSpace(seedPlan.SQL) != "" {
		return seedPlan, nil
	}
	if attempt == 1 {
		return planDatabaseReadQueryWithContext(ctx, s.planner(), question, inspection, s.chatHistory)
	}
	return repairDatabaseReadQuery(ctx, s.planner(), question, inspection, s.chatHistory, attempts)
}

func (s *databaseQuerySubAgent) planner() *ai.Client {
	if s.aiClient == nil {
		s.aiClient = newConfiguredAIClient(s.debug)
	}
	return s.aiClient
}

func planDatabaseReadQueryWithContext(ctx context.Context, aiClient *ai.Client, question string, inspection dbcontext.Inspection, chatHistory string) (databaseReadPlan, error) {
	prompt := fmt.Sprintf(`You are Clanker's database query subagent.

Prepare a single live read-only SQL query that can answer the user's current question.

Return strict JSON only with these keys:
- should_query: boolean
- sql: string
- reason: string

Rules:
- Only emit one SELECT statement, or one WITH ... SELECT statement.
- Never emit comments, semicolons, transaction control, EXPLAIN, PRAGMA, DDL, DML, CALL, COPY, ATTACH, DETACH, or any write operation.
- Use only the tables and columns shown in the schema summary.
- If the question asks for rankings, leaders, aggregates, comparisons, or "best/highest/most" entities, prefer a concrete aggregate query rather than a generic preview.
- Join tables only when the matching key columns are visible in the schema summary.
- If this connection cannot answer the question safely, set should_query to false and sql to an empty string.
- For "how many" or count questions, prefer COUNT(*).
- For record listings or searches, include LIMIT 50.
- Do not guess missing tables, columns, or filters.

Connection:
Name: %s
Kind: %s
Target: %s
Current Database: %s

Schema Summary:
%s

Recent Chat History:
%s

User Question:
%s`,
		inspection.Connection.Name,
		inspection.Connection.Kind(),
		inspection.Connection.Target(),
		inspection.CurrentDatabase,
		formatInspectionForQueryPlanning(inspection),
		defaultDatabaseChatHistory(chatHistory),
		strings.TrimSpace(question),
	)

	raw, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return databaseReadPlan{}, err
	}
	return parseDatabaseReadPlanResponse(aiClient, raw)
}

func repairDatabaseReadQuery(ctx context.Context, aiClient *ai.Client, question string, inspection dbcontext.Inspection, chatHistory string, attempts []databaseQueryAttempt) (databaseReadPlan, error) {
	prompt := fmt.Sprintf(`You are Clanker's database query evaluation subagent.

A previous live read-only SQL attempt failed. Review the failures, the schema context, and the recent conversation history, then propose a corrected single read-only SQL query.

Return strict JSON only with these keys:
- should_query: boolean
- sql: string
- reason: string

Rules:
- Only emit one SELECT statement, or one WITH ... SELECT statement.
- Never emit comments, semicolons, transaction control, EXPLAIN, PRAGMA, DDL, DML, CALL, COPY, ATTACH, DETACH, or any write operation.
- Use only the tables and columns shown in the schema summary.
- Fix the previous failure rather than repeating the same SQL.
- If the question asks for rankings, leaders, aggregates, comparisons, or "best/highest/most" entities, prefer a concrete aggregate query rather than a generic preview.
- Join tables only when the matching key columns are visible in the schema summary.
- If this connection still cannot answer the question safely, set should_query to false and sql to an empty string.
- For record listings or searches, include LIMIT 50.
- Do not guess missing tables, columns, or filters.

Connection:
Name: %s
Kind: %s
Target: %s
Current Database: %s

Schema Summary:
%s

Recent Chat History:
%s

User Question:
%s

Previous Attempts:
%s`,
		inspection.Connection.Name,
		inspection.Connection.Kind(),
		inspection.Connection.Target(),
		inspection.CurrentDatabase,
		formatInspectionForQueryPlanning(inspection),
		defaultDatabaseChatHistory(chatHistory),
		strings.TrimSpace(question),
		formatDatabaseQueryAttempts(attempts),
	)

	raw, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return databaseReadPlan{}, err
	}
	return parseDatabaseReadPlanResponse(aiClient, raw)
}

func parseDatabaseReadPlanResponse(aiClient *ai.Client, raw string) (databaseReadPlan, error) {
	cleaned := strings.TrimSpace(aiClient.CleanJSONResponse(raw))
	if cleaned == "" {
		cleaned = strings.TrimSpace(raw)
	}

	var plan databaseReadPlan
	if err := json.Unmarshal([]byte(cleaned), &plan); err != nil {
		return databaseReadPlan{}, fmt.Errorf("invalid live-query plan JSON: %w", err)
	}

	plan.SQL = strings.TrimSpace(plan.SQL)
	plan.Reason = strings.TrimSpace(plan.Reason)
	if !plan.ShouldQuery && plan.SQL != "" {
		plan.ShouldQuery = true
	}

	return plan, nil
}

func formatDatabaseQueryAttempts(attempts []databaseQueryAttempt) string {
	if len(attempts) == 0 {
		return "- no attempts recorded"
	}

	b := &strings.Builder{}
	for _, attempt := range attempts {
		b.WriteString(fmt.Sprintf("- Attempt %d\n", attempt.Attempt))
		if strings.TrimSpace(attempt.Reason) != "" {
			b.WriteString(fmt.Sprintf("  Reason: %s\n", strings.TrimSpace(attempt.Reason)))
		}
		if strings.TrimSpace(attempt.SQL) != "" {
			b.WriteString(fmt.Sprintf("  SQL: %s\n", strings.TrimSpace(attempt.SQL)))
		}
		if strings.TrimSpace(attempt.Error) != "" {
			b.WriteString(fmt.Sprintf("  Error: %s\n", strings.TrimSpace(attempt.Error)))
		}
	}
	return strings.TrimSpace(b.String())
}

func formatDatabaseRetryOutcome(connection dbcontext.Connection, attempts []databaseQueryAttempt, chatHistory string) string {
	b := &strings.Builder{}
	b.WriteString(fmt.Sprintf("Connection: %s [%s] %s\n", connection.Name, connection.Kind(), connection.Target()))
	b.WriteString(fmt.Sprintf("Max Attempts: %d\n", maxDatabaseQueryHealAttempts))
	b.WriteString(fmt.Sprintf("Chat History Considered: %t\n", strings.TrimSpace(chatHistory) != ""))
	b.WriteString("Attempts:\n")
	b.WriteString(formatDatabaseQueryAttempts(attempts))
	b.WriteString("\nResult: exhausted the database query self-heal budget and returned the last observed failure.")
	return b.String()
}

func defaultDatabaseChatHistory(chatHistory string) string {
	trimmed := strings.TrimSpace(chatHistory)
	if trimmed == "" {
		return "none provided"
	}
	return trimmed
}

func connectionNames(connections []dbcontext.Connection) []string {
	names := make([]string, 0, len(connections))
	for _, connection := range connections {
		trimmed := strings.TrimSpace(connection.Name)
		if trimmed == "" {
			continue
		}
		names = append(names, trimmed)
	}
	if len(names) == 0 {
		return []string{"configured connections"}
	}
	return names
}
