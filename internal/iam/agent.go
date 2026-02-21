package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/iam/analyzer"
	"github.com/bgdnvk/clanker/internal/iam/fixer"
	"github.com/spf13/viper"
)

// Agent orchestrates IAM operations
type Agent struct {
	client       *Client
	analyzer     *analyzer.SubAgent
	fixer        *fixer.SubAgent
	conversation *ConversationHistory
	debug        bool
}

// AgentOptions configures the IAM agent
type AgentOptions struct {
	Profile string
	Region  string
	Debug   bool
}

// NewAgentWithOptions creates a new IAM agent with the specified options
func NewAgentWithOptions(opts AgentOptions) (*Agent, error) {
	client, err := NewClient(opts.Profile, opts.Region, opts.Debug)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM client: %w", err)
	}

	accountID := client.GetAccountID()
	conversation := NewConversationHistory(accountID)
	if err := conversation.Load(); err != nil && opts.Debug {
		fmt.Printf("[iam] Warning: could not load conversation history: %v\n", err)
	}

	// Create adapters for the subagents
	analyzerClient := &analyzerClientAdapter{client: client}
	fixerClient := &fixerClientAdapter{client: client}

	return &Agent{
		client:       client,
		analyzer:     analyzer.NewSubAgent(analyzerClient, opts.Debug),
		fixer:        fixer.NewSubAgent(fixerClient, opts.Debug),
		conversation: conversation,
		debug:        opts.Debug,
	}, nil
}

// HandleQuery handles an IAM query and returns a response
func (a *Agent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if a.debug {
		fmt.Printf("[iam] Processing query: %s\n", query)
	}

	// Check for special commands
	queryLower := strings.ToLower(strings.TrimSpace(query))

	// Handle analyze requests
	if strings.Contains(queryLower, "analyze") || strings.Contains(queryLower, "security") {
		return a.handleAnalyzeRequest(ctx, query, opts)
	}

	// Handle fix requests
	if strings.Contains(queryLower, "fix") || strings.Contains(queryLower, "remediate") {
		return a.handleFixRequest(ctx, query, opts)
	}

	// Handle general queries
	return a.handleGeneralQuery(ctx, query, opts)
}

// handleAnalyzeRequest handles security analysis requests
func (a *Agent) handleAnalyzeRequest(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	var analyzerFindings []analyzer.SecurityFinding
	var err error

	if opts.RoleARN != "" {
		// Analyze specific role
		roleName := extractRoleName(opts.RoleARN)
		analyzerFindings, err = a.analyzer.AnalyzeRole(ctx, roleName)
	} else if opts.PolicyARN != "" {
		// Analyze specific policy
		analyzerFindings, err = a.analyzer.AnalyzePolicy(ctx, opts.PolicyARN)
	} else {
		// Account-wide analysis
		analyzerFindings, err = a.analyzer.AnalyzeAccount(ctx)
	}

	if err != nil {
		return &Response{
			Type:  ResponseTypeError,
			Error: err,
		}, nil
	}

	// Convert analyzer findings to main package type
	findings := convertAnalyzerFindings(analyzerFindings)

	// Format findings
	formattedFindings := analyzer.FormatFindings(analyzerFindings)

	// Save to conversation
	a.conversation.AddEntry(query, formattedFindings, a.client.GetAccountID())
	_ = a.conversation.Save()

	return &Response{
		Type:     ResponseTypeFindings,
		Content:  formattedFindings,
		Findings: findings,
	}, nil
}

// handleFixRequest handles fix/remediation requests
func (a *Agent) handleFixRequest(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	// First, run analysis to get findings
	var analyzerFindings []analyzer.SecurityFinding
	var err error

	if opts.RoleARN != "" {
		roleName := extractRoleName(opts.RoleARN)
		analyzerFindings, err = a.analyzer.AnalyzeRole(ctx, roleName)
	} else if opts.PolicyARN != "" {
		analyzerFindings, err = a.analyzer.AnalyzePolicy(ctx, opts.PolicyARN)
	} else {
		analyzerFindings, err = a.analyzer.AnalyzeAccount(ctx)
	}

	if err != nil {
		return &Response{
			Type:  ResponseTypeError,
			Error: err,
		}, nil
	}

	if len(analyzerFindings) == 0 {
		return &Response{
			Type:    ResponseTypeResult,
			Content: "No security findings to fix",
		}, nil
	}

	// Sort by severity and take the most critical finding
	analyzer.SortFindingsBySeverity(analyzerFindings)
	analyzerFinding := analyzerFindings[0]

	// Convert to fixer finding type
	fixerFinding := convertAnalyzerToFixerFinding(analyzerFinding)

	// Generate fix plan
	fixerPlan, err := a.fixer.GenerateFixPlan(ctx, fixerFinding)
	if err != nil {
		return &Response{
			Type:  ResponseTypeError,
			Error: fmt.Errorf("failed to generate fix plan: %w", err),
		}, nil
	}

	// Convert to main package plan type
	plan := convertFixerPlan(fixerPlan)

	// Format plan
	formattedPlan := fixer.FormatPlan(fixerPlan)

	// Save to conversation
	a.conversation.AddEntry(query, formattedPlan, a.client.GetAccountID())
	_ = a.conversation.Save()

	return &Response{
		Type:    ResponseTypePlan,
		Content: formattedPlan,
		Plan:    plan,
	}, nil
}

// handleGeneralQuery handles general IAM queries using LLM
func (a *Agent) handleGeneralQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	// Get account summary for context
	summary, err := a.client.GetAccountSummary(ctx)
	if err == nil {
		a.conversation.UpdateAccountSummary(summary)
	}

	// Build IAM context
	iamContext := a.conversation.GetAccountSummaryContext()
	conversationContext := a.conversation.GetRecentContext(5)

	// Get LLM to analyze what operations are needed
	analysisPrompt := GetLLMAnalysisPrompt(query, iamContext)

	aiClient := a.getAIClient()
	analysisResp, err := aiClient.AskPrompt(ctx, analysisPrompt)
	if err != nil {
		return &Response{
			Type:  ResponseTypeError,
			Error: fmt.Errorf("failed to analyze query: %w", err),
		}, nil
	}

	// Parse the LLM response
	analysisResp = aiClient.CleanJSONResponse(analysisResp)
	var analysis IAMAnalysis
	if err := json.Unmarshal([]byte(analysisResp), &analysis); err != nil {
		if a.debug {
			fmt.Printf("[iam] Failed to parse analysis: %v\nRaw response: %s\n", err, analysisResp)
		}
		// If parsing fails, return a basic response
		return &Response{
			Type:    ResponseTypeResult,
			Content: fmt.Sprintf("Unable to analyze query. Please try rephrasing: %s", query),
		}, nil
	}

	if a.debug {
		fmt.Printf("[iam] Analysis: %s\n", analysis.Analysis)
		fmt.Printf("[iam] Operations needed: %d\n", len(analysis.Operations))
	}

	// Execute the operations
	iamData, err := a.client.ExecuteOperations(ctx, analysis.Operations)
	if err != nil {
		return &Response{
			Type:  ResponseTypeError,
			Error: fmt.Errorf("failed to execute operations: %w", err),
		}, nil
	}

	// Generate final response using LLM
	finalPrompt := GetFinalResponsePrompt(query, iamData, conversationContext)
	finalResp, err := aiClient.AskPrompt(ctx, finalPrompt)
	if err != nil {
		// If final response fails, return the raw data
		return &Response{
			Type:    ResponseTypeResult,
			Content: iamData,
		}, nil
	}

	// Save to conversation
	a.conversation.AddEntry(query, finalResp, a.client.GetAccountID())
	_ = a.conversation.Save()

	return &Response{
		Type:    ResponseTypeResult,
		Content: finalResp,
	}, nil
}

// ApplyFixPlan applies a fix plan
func (a *Agent) ApplyFixPlan(ctx context.Context, plan *FixPlan, confirm bool) error {
	fixerPlan := convertToFixerPlan(plan)
	return a.fixer.ApplyPlan(ctx, fixerPlan, confirm)
}

// GetAnalyzer returns the analyzer subagent
func (a *Agent) GetAnalyzer() *analyzer.SubAgent {
	return a.analyzer
}

// GetFixer returns the fixer subagent
func (a *Agent) GetFixer() *fixer.SubAgent {
	return a.fixer
}

// GetClient returns the IAM client
func (a *Agent) GetClient() *Client {
	return a.client
}

// GetAccountID returns the AWS account ID
func (a *Agent) GetAccountID() string {
	return a.client.GetAccountID()
}

// ClearConversation clears the conversation history
func (a *Agent) ClearConversation() {
	a.conversation.Clear()
	_ = a.conversation.Save()
}

// getAIClient creates an AI client based on configuration
func (a *Agent) getAIClient() *ai.Client {
	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}

	var apiKey string
	switch provider {
	case "openai":
		apiKey = viper.GetString("ai.providers.openai.api_key")
	case "anthropic":
		apiKey = viper.GetString("ai.providers.anthropic.api_key")
	case "gemini", "gemini-api":
		apiKey = "" // Uses ADC
	}

	return ai.NewClient(provider, apiKey, a.debug, provider)
}

// extractRoleName extracts the role name from a role ARN
func extractRoleName(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return arn
}

// Type conversion functions

// convertAnalyzerFindings converts analyzer findings to main package type
func convertAnalyzerFindings(findings []analyzer.SecurityFinding) []SecurityFinding {
	result := make([]SecurityFinding, len(findings))
	for i, f := range findings {
		result[i] = SecurityFinding{
			ID:          f.ID,
			Severity:    f.Severity,
			Type:        f.Type,
			ResourceARN: f.ResourceARN,
			Description: f.Description,
			Remediation: f.Remediation,
			Actions:     f.Actions,
			Resources:   f.Resources,
		}
	}
	return result
}

// convertAnalyzerToFixerFinding converts an analyzer finding to fixer finding type
func convertAnalyzerToFixerFinding(f analyzer.SecurityFinding) fixer.SecurityFinding {
	return fixer.SecurityFinding{
		ID:          f.ID,
		Severity:    f.Severity,
		Type:        f.Type,
		ResourceARN: f.ResourceARN,
		Description: f.Description,
		Remediation: f.Remediation,
		Actions:     f.Actions,
		Resources:   f.Resources,
	}
}

// convertFixerPlan converts a fixer plan to main package type
func convertFixerPlan(plan *fixer.FixPlan) *FixPlan {
	if plan == nil {
		return nil
	}

	commands := make([]FixCommand, len(plan.Commands))
	for i, cmd := range plan.Commands {
		commands[i] = FixCommand{
			ID:          cmd.ID,
			Action:      cmd.Action,
			ResourceARN: cmd.ResourceARN,
			Parameters:  cmd.Parameters,
			Reason:      cmd.Reason,
		}
		if cmd.Rollback != nil {
			rollback := convertFixerCommand(cmd.Rollback)
			commands[i].Rollback = &rollback
		}
	}

	return &FixPlan{
		ID:      plan.ID,
		Summary: plan.Summary,
		Finding: SecurityFinding{
			ID:          plan.Finding.ID,
			Severity:    plan.Finding.Severity,
			Type:        plan.Finding.Type,
			ResourceARN: plan.Finding.ResourceARN,
			Description: plan.Finding.Description,
			Remediation: plan.Finding.Remediation,
			Actions:     plan.Finding.Actions,
			Resources:   plan.Finding.Resources,
		},
		Commands:  commands,
		Notes:     plan.Notes,
		Warnings:  plan.Warnings,
		CreatedAt: plan.CreatedAt,
	}
}

// convertFixerCommand converts a single fixer command to main package type
func convertFixerCommand(cmd *fixer.FixCommand) FixCommand {
	result := FixCommand{
		ID:          cmd.ID,
		Action:      cmd.Action,
		ResourceARN: cmd.ResourceARN,
		Parameters:  cmd.Parameters,
		Reason:      cmd.Reason,
	}
	if cmd.Rollback != nil {
		rollback := convertFixerCommand(cmd.Rollback)
		result.Rollback = &rollback
	}
	return result
}

// convertToFixerPlan converts a main package plan to fixer plan type
func convertToFixerPlan(plan *FixPlan) *fixer.FixPlan {
	if plan == nil {
		return nil
	}

	commands := make([]fixer.FixCommand, len(plan.Commands))
	for i, cmd := range plan.Commands {
		commands[i] = fixer.FixCommand{
			ID:          cmd.ID,
			Action:      cmd.Action,
			ResourceARN: cmd.ResourceARN,
			Parameters:  cmd.Parameters,
			Reason:      cmd.Reason,
		}
		if cmd.Rollback != nil {
			rollback := convertToFixerCommand(cmd.Rollback)
			commands[i].Rollback = &rollback
		}
	}

	return &fixer.FixPlan{
		ID:      plan.ID,
		Summary: plan.Summary,
		Finding: fixer.SecurityFinding{
			ID:          plan.Finding.ID,
			Severity:    plan.Finding.Severity,
			Type:        plan.Finding.Type,
			ResourceARN: plan.Finding.ResourceARN,
			Description: plan.Finding.Description,
			Remediation: plan.Finding.Remediation,
			Actions:     plan.Finding.Actions,
			Resources:   plan.Finding.Resources,
		},
		Commands:  commands,
		Notes:     plan.Notes,
		Warnings:  plan.Warnings,
		CreatedAt: plan.CreatedAt,
	}
}

// convertToFixerCommand converts a main package command to fixer command type
func convertToFixerCommand(cmd *FixCommand) fixer.FixCommand {
	result := fixer.FixCommand{
		ID:          cmd.ID,
		Action:      cmd.Action,
		ResourceARN: cmd.ResourceARN,
		Parameters:  cmd.Parameters,
		Reason:      cmd.Reason,
	}
	if cmd.Rollback != nil {
		rollback := convertToFixerCommand(cmd.Rollback)
		result.Rollback = &rollback
	}
	return result
}

// Client adapters for subagents

// analyzerClientAdapter adapts the IAM Client to the analyzer.IAMClient interface
type analyzerClientAdapter struct {
	client *Client
}

func (a *analyzerClientAdapter) ListRoles(ctx interface{}) ([]analyzer.RoleInfo, error) {
	roles, err := a.client.ListRoles(ctx.(context.Context))
	if err != nil {
		return nil, err
	}
	result := make([]analyzer.RoleInfo, len(roles))
	for i, r := range roles {
		result[i] = analyzer.RoleInfo{
			RoleName:                 r.RoleName,
			RoleARN:                  r.RoleARN,
			Path:                     r.Path,
			CreateDate:               r.CreateDate,
			AssumeRolePolicyDocument: r.AssumeRolePolicyDocument,
		}
	}
	return result, nil
}

func (a *analyzerClientAdapter) ListPolicies(ctx interface{}) ([]analyzer.PolicyInfo, error) {
	policies, err := a.client.ListPolicies(ctx.(context.Context))
	if err != nil {
		return nil, err
	}
	result := make([]analyzer.PolicyInfo, len(policies))
	for i, p := range policies {
		result[i] = analyzer.PolicyInfo{
			PolicyName: p.PolicyName,
			PolicyARN:  p.PolicyARN,
		}
	}
	return result, nil
}

func (a *analyzerClientAdapter) GetRoleDetails(ctx interface{}, roleName string) (*analyzer.RoleDetail, error) {
	detail, err := a.client.GetRoleDetails(ctx.(context.Context), roleName)
	if err != nil {
		return nil, err
	}

	attachedPolicies := make([]analyzer.PolicyInfo, len(detail.AttachedPolicies))
	for i, p := range detail.AttachedPolicies {
		attachedPolicies[i] = analyzer.PolicyInfo{
			PolicyName: p.PolicyName,
			PolicyARN:  p.PolicyARN,
		}
	}

	inlinePolicies := make([]analyzer.InlinePolicy, len(detail.InlinePolicies))
	for i, p := range detail.InlinePolicies {
		inlinePolicies[i] = analyzer.InlinePolicy{
			PolicyName:     p.PolicyName,
			PolicyDocument: p.PolicyDocument,
		}
	}

	return &analyzer.RoleDetail{
		RoleInfo: analyzer.RoleInfo{
			RoleName:                 detail.RoleName,
			RoleARN:                  detail.RoleARN,
			Path:                     detail.Path,
			CreateDate:               detail.CreateDate,
			AssumeRolePolicyDocument: detail.AssumeRolePolicyDocument,
		},
		AttachedPolicies: attachedPolicies,
		InlinePolicies:   inlinePolicies,
		LastUsed:         detail.LastUsed,
	}, nil
}

func (a *analyzerClientAdapter) GetPolicyDocument(ctx interface{}, policyARN string) (*analyzer.PolicyDetail, error) {
	detail, err := a.client.GetPolicyDocument(ctx.(context.Context), policyARN)
	if err != nil {
		return nil, err
	}
	return &analyzer.PolicyDetail{
		PolicyInfo: analyzer.PolicyInfo{
			PolicyName: detail.PolicyName,
			PolicyARN:  detail.PolicyARN,
		},
		PolicyDocument: detail.PolicyDocument,
	}, nil
}

func (a *analyzerClientAdapter) GetCredentialReport(ctx interface{}) (*analyzer.CredentialReport, error) {
	report, err := a.client.GetCredentialReport(ctx.(context.Context))
	if err != nil {
		return nil, err
	}

	users := make([]analyzer.CredentialReportEntry, len(report.Users))
	for i, u := range report.Users {
		users[i] = analyzer.CredentialReportEntry{
			User:                   u.User,
			ARN:                    u.ARN,
			UserCreationTime:       u.UserCreationTime,
			PasswordEnabled:        u.PasswordEnabled,
			PasswordLastUsed:       u.PasswordLastUsed,
			MFAActive:              u.MFAActive,
			AccessKey1Active:       u.AccessKey1Active,
			AccessKey1LastRotated:  u.AccessKey1LastRotated,
			AccessKey1LastUsedDate: u.AccessKey1LastUsedDate,
			AccessKey2Active:       u.AccessKey2Active,
			AccessKey2LastRotated:  u.AccessKey2LastRotated,
			AccessKey2LastUsedDate: u.AccessKey2LastUsedDate,
		}
	}

	return &analyzer.CredentialReport{
		GeneratedTime: report.GeneratedTime,
		Users:         users,
	}, nil
}

// fixerClientAdapter adapts the IAM Client to the fixer.IAMClient interface
type fixerClientAdapter struct {
	client *Client
}

func (f *fixerClientAdapter) GetRoleDetails(ctx interface{}, roleName string) (*fixer.RoleDetail, error) {
	detail, err := f.client.GetRoleDetails(ctx.(context.Context), roleName)
	if err != nil {
		return nil, err
	}

	attachedPolicies := make([]fixer.PolicyInfo, len(detail.AttachedPolicies))
	for i, p := range detail.AttachedPolicies {
		attachedPolicies[i] = fixer.PolicyInfo{
			PolicyName: p.PolicyName,
			PolicyARN:  p.PolicyARN,
		}
	}

	inlinePolicies := make([]fixer.InlinePolicy, len(detail.InlinePolicies))
	for i, p := range detail.InlinePolicies {
		inlinePolicies[i] = fixer.InlinePolicy{
			PolicyName:     p.PolicyName,
			PolicyDocument: p.PolicyDocument,
		}
	}

	return &fixer.RoleDetail{
		RoleName:                 detail.RoleName,
		RoleARN:                  detail.RoleARN,
		AssumeRolePolicyDocument: detail.AssumeRolePolicyDocument,
		AttachedPolicies:         attachedPolicies,
		InlinePolicies:           inlinePolicies,
		LastUsed:                 detail.LastUsed,
	}, nil
}

func (f *fixerClientAdapter) GetPolicyDocument(ctx interface{}, policyARN string) (*fixer.PolicyDetail, error) {
	detail, err := f.client.GetPolicyDocument(ctx.(context.Context), policyARN)
	if err != nil {
		return nil, err
	}
	return &fixer.PolicyDetail{
		PolicyDocument: detail.PolicyDocument,
	}, nil
}

func (f *fixerClientAdapter) ListAccessKeys(ctx interface{}, userName string) ([]fixer.AccessKeyInfo, error) {
	keys, err := f.client.ListAccessKeys(ctx.(context.Context), userName)
	if err != nil {
		return nil, err
	}
	result := make([]fixer.AccessKeyInfo, len(keys))
	for i, k := range keys {
		result[i] = fixer.AccessKeyInfo{
			UserName:        k.UserName,
			AccessKeyId:     k.AccessKeyId,
			Status:          k.Status,
			CreateDate:      k.CreateDate,
			LastUsedDate:    k.LastUsedDate,
			LastUsedService: k.LastUsedService,
			LastUsedRegion:  k.LastUsedRegion,
		}
	}
	return result, nil
}

func (f *fixerClientAdapter) CreatePolicyVersion(ctx interface{}, policyARN, document string, setAsDefault bool) error {
	return f.client.CreatePolicyVersion(ctx.(context.Context), policyARN, document, setAsDefault)
}

func (f *fixerClientAdapter) UpdateAssumeRolePolicy(ctx interface{}, roleName, document string) error {
	return f.client.UpdateAssumeRolePolicy(ctx.(context.Context), roleName, document)
}

func (f *fixerClientAdapter) AttachRolePolicy(ctx interface{}, roleName, policyARN string) error {
	return f.client.AttachRolePolicy(ctx.(context.Context), roleName, policyARN)
}

func (f *fixerClientAdapter) DetachRolePolicy(ctx interface{}, roleName, policyARN string) error {
	return f.client.DetachRolePolicy(ctx.(context.Context), roleName, policyARN)
}

func (f *fixerClientAdapter) UpdateAccessKey(ctx interface{}, userName, accessKeyID, status string) error {
	return f.client.UpdateAccessKey(ctx.(context.Context), userName, accessKeyID, status)
}
