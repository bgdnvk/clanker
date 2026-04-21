package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

type Repository struct {
	Owner         string
	Name          string
	Description   string
	URL           string
	DefaultBranch string
	IsPrivate     bool
	IsFork        bool
}

type Runner struct {
	ID     int64
	Name   string
	OS     string
	Status string
	Busy   bool
	Labels []string
}

type AuthStatus struct {
	CLIAvailable   bool
	Authenticated  bool
	Login          string
	CopilotEnabled bool
}

type workflowRunSummary struct {
	ID           int64
	RunNumber    int
	Name         string
	DisplayTitle string
	Status       string
	Conclusion   string
	HeadBranch   string
	Event        string
	HTMLURL      string
	LogsURL      string
	ArtifactsURL string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Actor        string
}

type workflowJobSummary struct {
	ID          int64
	Name        string
	Status      string
	Conclusion  string
	HTMLURL     string
	CheckRunURL string
	Steps       []workflowJobStepSummary
}

type workflowJobStepSummary struct {
	Number     int
	Name       string
	Status     string
	Conclusion string
}

type workflowArtifactSummary struct {
	ID                 int64
	Name               string
	SizeInBytes        int64
	Expired            bool
	CreatedAt          time.Time
	ExpiresAt          time.Time
	ArchiveDownloadURL string
}

type workflowAnnotationSummary struct {
	Path            string
	StartLine       int
	EndLine         int
	AnnotationLevel string
	Message         string
	Title           string
}

type workflowSecuritySummary struct {
	Name                   string
	Path                   string
	Events                 []string
	Permissions            string
	UsesPullRequestTarget  bool
	UsesRepositoryDispatch bool
	UsesWorkflowDispatch   bool
	UsesSchedule           bool
}

type Client struct {
	client       *github.Client
	owner        string
	repo         string
	resolvedOnce bool
}

func NewClient(token, owner, repo string) *Client {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		trimmedToken = resolveGitHubToken(context.Background())
	}

	var client *github.Client
	if trimmedToken != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: trimmedToken})
		tc := oauth2.NewClient(context.Background(), ts)
		client = github.NewClient(tc)
	} else {
		client = github.NewClient(nil)
	}

	return &Client{
		client: client,
		owner:  strings.TrimSpace(owner),
		repo:   strings.TrimSpace(repo),
	}
}

func resolveGitHubToken(ctx context.Context) string {
	output, err := runCommand(ctx, "gh", "auth", "token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func runGitHubCLIWithRetry(ctx context.Context, args ...string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		output, err := runCommand(ctx, "gh", args...)
		if err == nil {
			return output, nil
		}
		lastErr = err
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "http 502") && !strings.Contains(message, "bad gateway") && !strings.Contains(message, "http 503") {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
	}
	return "", lastErr
}

func hasUsableCopilotCLI(ctx context.Context) bool {
	if _, err := exec.LookPath("copilot"); err != nil {
		return false
	}
	output, err := runCommand(ctx, "copilot", "--help")
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "cannot find github copilot cli") || strings.Contains(lower, "install github copilot cli") {
		return false
	}
	return true
}

func (c *Client) ResolveRepository(ctx context.Context) (string, string, error) {
	if strings.TrimSpace(c.owner) != "" && strings.TrimSpace(c.repo) != "" {
		return c.owner, c.repo, nil
	}
	if c.resolvedOnce {
		return c.owner, c.repo, nil
	}
	c.resolvedOnce = true

	type repoView struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}

	output, err := runCommand(ctx, "gh", "repo", "view", "--json", "name,owner")
	if err != nil {
		return "", "", err
	}

	var view repoView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return "", "", fmt.Errorf("failed to parse gh repo view output: %w", err)
	}

	c.owner = strings.TrimSpace(view.Owner.Login)
	c.repo = strings.TrimSpace(view.Name)
	if c.owner == "" || c.repo == "" {
		return "", "", fmt.Errorf("unable to resolve current GitHub repository")
	}

	return c.owner, c.repo, nil
}

func (c *Client) GetAuthStatus(ctx context.Context) AuthStatus {
	status := AuthStatus{}
	if _, err := exec.LookPath("gh"); err != nil {
		return status
	}
	status.CLIAvailable = true
	if token := resolveGitHubToken(ctx); token != "" {
		status.Authenticated = true
	}

	if output, err := runCommand(ctx, "gh", "api", "user"); err == nil {
		var user struct {
			Login string `json:"login"`
		}
		if json.Unmarshal([]byte(output), &user) == nil {
			status.Login = strings.TrimSpace(user.Login)
		}
	}

	if hasUsableCopilotCLI(ctx) {
		status.CopilotEnabled = true
	}

	return status
}

func (c *Client) ListRepositories(ctx context.Context, limit int) ([]Repository, error) {
	if limit <= 0 {
		limit = 100
	}

	output, err := runGitHubCLIWithRetry(ctx, "repo", "list", "--limit", fmt.Sprintf("%d", limit), "--json", "name,owner,description,url,isPrivate,isFork,defaultBranchRef")
	if err != nil {
		fallbackRepos, fallbackErr := c.listViewerRepositories(ctx, limit)
		if fallbackErr == nil {
			return fallbackRepos, nil
		}
		return nil, fmt.Errorf("list repositories failed via gh repo list: %w; fallback failed: %v", err, fallbackErr)
	}

	var raw []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
		IsPrivate   bool   `json:"isPrivate"`
		IsFork      bool   `json:"isFork"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
		DefaultBranchRef *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}

	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse gh repo list output: %w", err)
	}

	repos := make([]Repository, 0, len(raw))
	for _, item := range raw {
		defaultBranch := ""
		if item.DefaultBranchRef != nil {
			defaultBranch = strings.TrimSpace(item.DefaultBranchRef.Name)
		}
		repos = append(repos, Repository{
			Owner:         strings.TrimSpace(item.Owner.Login),
			Name:          strings.TrimSpace(item.Name),
			Description:   strings.TrimSpace(item.Description),
			URL:           strings.TrimSpace(item.URL),
			DefaultBranch: defaultBranch,
			IsPrivate:     item.IsPrivate,
			IsFork:        item.IsFork,
		})
	}

	return repos, nil
}

func (c *Client) listViewerRepositories(ctx context.Context, limit int) ([]Repository, error) {
	pageSize := limit
	if pageSize > 100 {
		pageSize = 100
	}

	type viewerRepo struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		HTMLURL       string `json:"html_url"`
		Private       bool   `json:"private"`
		Fork          bool   `json:"fork"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}

	repositoriesByFullName := make(map[string]Repository)
	for page := 1; len(repositoriesByFullName) < limit; page++ {
		output, err := runGitHubCLIWithRetry(ctx, "api", "user/repos", "--method", "GET", "-F", fmt.Sprintf("per_page=%d", pageSize), "-F", fmt.Sprintf("page=%d", page), "-F", "sort=updated")
		if err != nil {
			return nil, err
		}

		var raw []viewerRepo
		if err := json.Unmarshal([]byte(output), &raw); err != nil {
			return nil, fmt.Errorf("failed to parse gh api user/repos output: %w", err)
		}
		if len(raw) == 0 {
			break
		}

		for _, item := range raw {
			owner := strings.TrimSpace(item.Owner.Login)
			name := strings.TrimSpace(item.Name)
			if owner == "" || name == "" {
				continue
			}
			fullName := owner + "/" + name
			repositoriesByFullName[fullName] = Repository{
				Owner:         owner,
				Name:          name,
				Description:   strings.TrimSpace(item.Description),
				URL:           strings.TrimSpace(item.HTMLURL),
				DefaultBranch: strings.TrimSpace(item.DefaultBranch),
				IsPrivate:     item.Private,
				IsFork:        item.Fork,
			}
			if len(repositoriesByFullName) >= limit {
				break
			}
		}

		if len(raw) < pageSize {
			break
		}
	}

	if len(repositoriesByFullName) == 0 {
		return nil, fmt.Errorf("no repositories returned from GitHub API")
	}

	fullNames := make([]string, 0, len(repositoriesByFullName))
	for fullName := range repositoriesByFullName {
		fullNames = append(fullNames, fullName)
	}
	sort.Strings(fullNames)

	repositories := make([]Repository, 0, len(fullNames))
	for _, fullName := range fullNames {
		repositories = append(repositories, repositoriesByFullName[fullName])
		if len(repositories) >= limit {
			break
		}
	}

	return repositories, nil
}

func (c *Client) FormatStatus(ctx context.Context) (string, error) {
	status := c.GetAuthStatus(ctx)
	if !status.CLIAvailable {
		return "GitHub CLI: not installed\n", nil
	}

	var info strings.Builder
	info.WriteString("GitHub CLI: installed\n")
	if status.Authenticated {
		info.WriteString("Authentication: authenticated\n")
	} else {
		info.WriteString("Authentication: not authenticated\n")
	}
	if status.Login != "" {
		info.WriteString(fmt.Sprintf("Login: %s\n", status.Login))
	}
	if status.CopilotEnabled {
		info.WriteString("Copilot CLI (copilot): available\n")
	} else {
		info.WriteString("Copilot CLI (copilot): unavailable\n")
	}

	owner, repo, err := c.ResolveRepository(ctx)
	if err == nil && owner != "" && repo != "" {
		info.WriteString(fmt.Sprintf("Current repository: %s/%s\n", owner, repo))
	} else {
		info.WriteString("Current repository: unresolved\n")
	}

	return info.String(), nil
}

func (c *Client) ListRunners(ctx context.Context) ([]Runner, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return nil, err
	}

	output, err := runCommand(ctx, "gh", "api", fmt.Sprintf("repos/%s/%s/actions/runners", owner, repo))
	if err != nil {
		return nil, err
	}

	var raw struct {
		Runners []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			OS     string `json:"os"`
			Status string `json:"status"`
			Busy   bool   `json:"busy"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"runners"`
	}

	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub runners: %w", err)
	}

	runners := make([]Runner, 0, len(raw.Runners))
	for _, item := range raw.Runners {
		labels := make([]string, 0, len(item.Labels))
		for _, label := range item.Labels {
			if trimmed := strings.TrimSpace(label.Name); trimmed != "" {
				labels = append(labels, trimmed)
			}
		}
		runners = append(runners, Runner{
			ID:     item.ID,
			Name:   strings.TrimSpace(item.Name),
			OS:     strings.TrimSpace(item.OS),
			Status: strings.TrimSpace(item.Status),
			Busy:   item.Busy,
			Labels: labels,
		})
	}

	return runners, nil
}

func FormatRepositories(repos []Repository) string {
	if len(repos) == 0 {
		return "No GitHub repositories found.\n"
	}

	var info strings.Builder
	for _, repo := range repos {
		visibility := "public"
		if repo.IsPrivate {
			visibility = "private"
		}
		forkSuffix := ""
		if repo.IsFork {
			forkSuffix = ", fork"
		}
		info.WriteString(fmt.Sprintf("- %s/%s (%s%s)", repo.Owner, repo.Name, visibility, forkSuffix))
		if repo.DefaultBranch != "" {
			info.WriteString(fmt.Sprintf(", default branch: %s", repo.DefaultBranch))
		}
		if repo.Description != "" {
			info.WriteString(fmt.Sprintf(", %s", repo.Description))
		}
		if repo.URL != "" {
			info.WriteString(fmt.Sprintf(", URL: %s", repo.URL))
		}
		info.WriteString("\n")
	}
	return info.String()
}

func FormatRunners(runners []Runner) string {
	if len(runners) == 0 {
		return "No self-hosted runners found for this repository.\n"
	}

	var info strings.Builder
	for _, runner := range runners {
		busyState := "idle"
		if runner.Busy {
			busyState = "busy"
		}
		info.WriteString(fmt.Sprintf("- %s (%s, %s, %s)", runner.Name, runner.OS, runner.Status, busyState))
		if len(runner.Labels) > 0 {
			info.WriteString(fmt.Sprintf(", labels: %s", strings.Join(runner.Labels, ", ")))
		}
		info.WriteString("\n")
	}
	return info.String()
}

func containsAnyGitHubPhrase(input string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(input, strings.ToLower(strings.TrimSpace(phrase))) {
			return true
		}
	}
	return false
}

func formatWorkflowRunLabel(run workflowRunSummary) string {
	if strings.TrimSpace(run.DisplayTitle) != "" {
		return strings.TrimSpace(run.DisplayTitle)
	}
	if strings.TrimSpace(run.Name) != "" {
		return strings.TrimSpace(run.Name)
	}
	return "workflow run"
}

func workflowRunNeedsAttention(run workflowRunSummary) bool {
	status := strings.ToLower(strings.TrimSpace(run.Status))
	if status != "" && status != "completed" {
		return true
	}
	conclusion := strings.ToLower(strings.TrimSpace(run.Conclusion))
	switch conclusion {
	case "", "success", "neutral", "skipped":
		return false
	default:
		return true
	}
}

func workflowJobNeedsAttention(job workflowJobSummary) bool {
	status := strings.ToLower(strings.TrimSpace(job.Status))
	if status != "" && status != "completed" {
		return true
	}
	conclusion := strings.ToLower(strings.TrimSpace(job.Conclusion))
	switch conclusion {
	case "", "success", "neutral", "skipped":
		return false
	default:
		return true
	}
}

func failedWorkflowJobSteps(steps []workflowJobStepSummary) []workflowJobStepSummary {
	failed := make([]workflowJobStepSummary, 0, len(steps))
	for _, step := range steps {
		status := strings.ToLower(strings.TrimSpace(step.Status))
		conclusion := strings.ToLower(strings.TrimSpace(step.Conclusion))
		if status == "completed" && (conclusion == "success" || conclusion == "skipped") {
			continue
		}
		if status == "" && conclusion == "" {
			continue
		}
		failed = append(failed, step)
	}
	return failed
}

func parseCheckRunID(checkRunURL string) int64 {
	trimmed := strings.TrimSpace(checkRunURL)
	if trimmed == "" {
		return 0
	}
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 || idx >= len(trimmed)-1 {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(trimmed[idx+1:]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func selectWorkflowRunsForDetail(runs []workflowRunSummary, limit int) []workflowRunSummary {
	if limit <= 0 || len(runs) == 0 {
		return nil
	}
	selected := make([]workflowRunSummary, 0, limit)
	seen := make(map[int64]struct{}, limit)
	appendRun := func(run workflowRunSummary) bool {
		if run.ID == 0 {
			return false
		}
		if _, ok := seen[run.ID]; ok {
			return false
		}
		seen[run.ID] = struct{}{}
		selected = append(selected, run)
		return len(selected) >= limit
	}
	for _, run := range runs {
		if workflowRunNeedsAttention(run) && appendRun(run) {
			return selected
		}
	}
	for _, run := range runs {
		if appendRun(run) {
			return selected
		}
	}
	return selected
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return "", err
	}
	c.owner = owner
	c.repo = repo

	var contextText strings.Builder
	questionLower := strings.ToLower(question)
	includeDetailedRunContext := containsAnyGitHubPhrase(questionLower,
		"job", "jobs", "step", "steps", "failed", "failure", "failing", "annotation", "annotations", "artifact", "artifacts", "log", "logs", "debug",
	)
	includeSecurityContext := containsAnyGitHubPhrase(questionLower,
		"security", "harden", "hardening", "branch protection", "protected branch", "workflow permission", "workflow permissions",
		"pull_request_target", "repository_dispatch", "workflow_dispatch", "schedule", "trigger", "triggers",
	)

	if strings.Contains(questionLower, "repo") || strings.Contains(questionLower, "repository") {
		repos, err := c.ListRepositories(ctx, 25)
		if err == nil {
			contextText.WriteString("GitHub Repositories:\n")
			contextText.WriteString(FormatRepositories(repos))
			contextText.WriteString("\n")
		}
	}

	if strings.Contains(questionLower, "runner") || strings.Contains(questionLower, "actions runner") {
		runners, err := c.ListRunners(ctx)
		if err == nil {
			contextText.WriteString("GitHub Runners:\n")
			contextText.WriteString(FormatRunners(runners))
			contextText.WriteString("\n")
		}
	}

	if includeSecurityContext {
		repoSecurity, err := c.getRepositorySecurityInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get repository security info: %w", err)
		}
		if strings.TrimSpace(repoSecurity) != "" {
			contextText.WriteString("GitHub Repository Security:\n")
			contextText.WriteString(repoSecurity)
			contextText.WriteString("\n\n")
		}

		branchProtection, err := c.getBranchProtectionInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get branch protection info: %w", err)
		}
		if strings.TrimSpace(branchProtection) != "" {
			contextText.WriteString("GitHub Branch Protection:\n")
			contextText.WriteString(branchProtection)
			contextText.WriteString("\n\n")
		}

		actionsPermissions, err := c.getActionsPermissionsInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get actions permissions info: %w", err)
		}
		if strings.TrimSpace(actionsPermissions) != "" {
			contextText.WriteString("GitHub Actions Permissions:\n")
			contextText.WriteString(actionsPermissions)
			contextText.WriteString("\n\n")
		}

		workflowSecurity, err := c.getWorkflowSecurityInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow security info: %w", err)
		}
		if strings.TrimSpace(workflowSecurity) != "" {
			contextText.WriteString("GitHub Workflow Trigger Review:\n")
			contextText.WriteString(workflowSecurity)
			contextText.WriteString("\n\n")
		}
	}

	if strings.Contains(questionLower, "action") || strings.Contains(questionLower, "workflow") || strings.Contains(questionLower, "ci") || strings.Contains(questionLower, "build") {
		workflowInfo, err := c.getWorkflowInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow info: %w", err)
		}
		contextText.WriteString("GitHub Actions Workflows:\n")
		contextText.WriteString(workflowInfo)
		contextText.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "run") || strings.Contains(questionLower, "execution") || strings.Contains(questionLower, "status") {
		runsInfo, err := c.getWorkflowRunsInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow runs info: %w", err)
		}
		contextText.WriteString("Recent Workflow Runs:\n")
		contextText.WriteString(runsInfo)
		contextText.WriteString("\n\n")
	}

	if includeDetailedRunContext {
		detailInfo, err := c.getWorkflowRunDetailsInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow run detail info: %w", err)
		}
		contextText.WriteString("Workflow Run Details:\n")
		contextText.WriteString(detailInfo)
		contextText.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "pull") || strings.Contains(questionLower, "pr") {
		prInfo, err := c.getPullRequestInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get pull request info: %w", err)
		}
		contextText.WriteString("Recent Pull Requests:\n")
		contextText.WriteString(prInfo)
		contextText.WriteString("\n\n")
	}

	return contextText.String(), nil
}

func (c *Client) getRepositorySecurityInfo(ctx context.Context) (string, error) {
	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s", c.owner, c.repo))
	if err != nil {
		return "", err
	}

	var raw struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		Fork          bool   `json:"fork"`
		AllowForking  bool   `json:"allow_forking"`
		Archived      bool   `json:"archived"`
		Disabled      bool   `json:"disabled"`
		Visibility    string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return "", fmt.Errorf("failed to parse repository security info: %w", err)
	}
	if strings.TrimSpace(raw.Visibility) == "" {
		if raw.Private {
			raw.Visibility = "private"
		} else {
			raw.Visibility = "public"
		}
	}

	return fmt.Sprintf("- %s, visibility=%s, default_branch=%s, allow_forking=%t, archived=%t, disabled=%t, fork=%t", strings.TrimSpace(raw.FullName), strings.TrimSpace(raw.Visibility), strings.TrimSpace(raw.DefaultBranch), raw.AllowForking, raw.Archived, raw.Disabled, raw.Fork), nil
}

func (c *Client) getBranchProtectionInfo(ctx context.Context) (string, error) {
	defaultBranch, err := c.defaultBranchName(ctx)
	if err != nil {
		return "", err
	}

	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/branches/%s/protection", c.owner, c.repo, defaultBranch))
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "404") || strings.Contains(lower, "branch not protected") || strings.Contains(lower, "not found") {
			return fmt.Sprintf("- Default branch %s protected=false", defaultBranch), nil
		}
		return "", err
	}

	var raw struct {
		RequiredPullRequestReviews struct {
			RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
			RequireCodeOwnerReviews      bool `json:"require_code_owner_reviews"`
		} `json:"required_pull_request_reviews"`
		RequiredStatusChecks struct {
			Strict   bool     `json:"strict"`
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
		EnforceAdmins struct {
			Enabled bool `json:"enabled"`
		} `json:"enforce_admins"`
		AllowForcePushes struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_force_pushes"`
		AllowDeletions struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_deletions"`
		RequiredLinearHistory struct {
			Enabled bool `json:"enabled"`
		} `json:"required_linear_history"`
		RequiredConversationResolution struct {
			Enabled bool `json:"enabled"`
		} `json:"required_conversation_resolution"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return "", fmt.Errorf("failed to parse branch protection info: %w", err)
	}

	statusChecks := "none"
	if len(raw.RequiredStatusChecks.Contexts) > 0 {
		statusChecks = strings.Join(raw.RequiredStatusChecks.Contexts, ",")
	}

	return fmt.Sprintf("- Default branch %s protected=true, required_reviews=%d, require_code_owner_reviews=%t, strict_status_checks=%t, status_checks=%s, enforce_admins=%t, allows_force_pushes=%t, allows_deletions=%t, requires_linear_history=%t, requires_conversation_resolution=%t", defaultBranch, raw.RequiredPullRequestReviews.RequiredApprovingReviewCount, raw.RequiredPullRequestReviews.RequireCodeOwnerReviews, raw.RequiredStatusChecks.Strict, statusChecks, raw.EnforceAdmins.Enabled, raw.AllowForcePushes.Enabled, raw.AllowDeletions.Enabled, raw.RequiredLinearHistory.Enabled, raw.RequiredConversationResolution.Enabled), nil
}

func (c *Client) getActionsPermissionsInfo(ctx context.Context) (string, error) {
	permissionsOutput, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/actions/permissions", c.owner, c.repo))
	if err != nil {
		return "", err
	}

	workflowOutput, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/actions/permissions/workflow", c.owner, c.repo))
	if err != nil {
		return "", err
	}

	var permissions struct {
		Enabled        bool   `json:"enabled"`
		AllowedActions string `json:"allowed_actions"`
	}
	if err := json.Unmarshal([]byte(permissionsOutput), &permissions); err != nil {
		return "", fmt.Errorf("failed to parse actions permissions: %w", err)
	}

	var workflowPermissions struct {
		DefaultWorkflowPermissions   string `json:"default_workflow_permissions"`
		CanApprovePullRequestReviews bool   `json:"can_approve_pull_request_reviews"`
	}
	if err := json.Unmarshal([]byte(workflowOutput), &workflowPermissions); err != nil {
		return "", fmt.Errorf("failed to parse workflow permissions: %w", err)
	}

	return strings.Join([]string{
		fmt.Sprintf("- Actions enabled=%t, allowed_actions=%s", permissions.Enabled, strings.TrimSpace(permissions.AllowedActions)),
		fmt.Sprintf("- default_workflow_permissions=%s, can_approve_pull_request_reviews=%t", strings.TrimSpace(workflowPermissions.DefaultWorkflowPermissions), workflowPermissions.CanApprovePullRequestReviews),
	}, "\n"), nil
}

func (c *Client) getWorkflowSecurityInfo(ctx context.Context) (string, error) {
	summaries, err := c.listWorkflowSecuritySummaries(ctx)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "No workflow files found.\n", nil
	}

	var info strings.Builder
	for _, summary := range summaries {
		permissions := strings.TrimSpace(summary.Permissions)
		if permissions == "" {
			permissions = "repo-default"
		}
		events := strings.Join(summary.Events, ",")
		if events == "" {
			events = "unknown"
		}
		info.WriteString(fmt.Sprintf("- Workflow: %s, path=%s, events=%s, permissions=%s, uses_pull_request_target=%t, uses_repository_dispatch=%t, uses_workflow_dispatch=%t, uses_schedule=%t\n", summary.Name, summary.Path, events, permissions, summary.UsesPullRequestTarget, summary.UsesRepositoryDispatch, summary.UsesWorkflowDispatch, summary.UsesSchedule))
	}

	return strings.TrimSpace(info.String()), nil
}

func (c *Client) defaultBranchName(ctx context.Context) (string, error) {
	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s", c.owner, c.repo))
	if err != nil {
		return "", err
	}
	var raw struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return "", fmt.Errorf("failed to parse default branch: %w", err)
	}
	if strings.TrimSpace(raw.DefaultBranch) == "" {
		return "", fmt.Errorf("default branch is empty")
	}
	return strings.TrimSpace(raw.DefaultBranch), nil
}

func (c *Client) listWorkflowSecuritySummaries(ctx context.Context) ([]workflowSecuritySummary, error) {
	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/contents/.github/workflows", c.owner, c.repo))
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "404") || strings.Contains(lower, "not found") {
			return nil, nil
		}
		return nil, err
	}

	var files []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(output), &files); err != nil {
		return nil, fmt.Errorf("failed to parse workflow file listing: %w", err)
	}

	summaries := make([]workflowSecuritySummary, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Type) != "file" {
			continue
		}
		lowerName := strings.ToLower(strings.TrimSpace(file.Name))
		if !strings.HasSuffix(lowerName, ".yml") && !strings.HasSuffix(lowerName, ".yaml") {
			continue
		}
		contentOutput, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/contents/%s", c.owner, c.repo, strings.TrimSpace(file.Path)))
		if err != nil {
			return nil, err
		}
		var content struct {
			Name     string `json:"name"`
			Path     string `json:"path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal([]byte(contentOutput), &content); err != nil {
			return nil, fmt.Errorf("failed to parse workflow file content: %w", err)
		}
		decoded, err := decodeGitHubContent(content.Content, content.Encoding)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summarizeGitHubWorkflowSecurity(content.Name, content.Path, decoded))
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Path < summaries[j].Path
	})
	return summaries, nil
}

func decodeGitHubContent(content string, encoding string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", nil
	}
	if strings.EqualFold(strings.TrimSpace(encoding), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(trimmed, "\n", ""))
		if err != nil {
			return "", fmt.Errorf("failed to decode GitHub content: %w", err)
		}
		return string(decoded), nil
	}
	return trimmed, nil
}

func summarizeGitHubWorkflowSecurity(name string, path string, content string) workflowSecuritySummary {
	workflow := workflowSecuritySummary{Name: strings.TrimSpace(name), Path: strings.TrimSpace(path)}
	var doc map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return workflow
	}

	triggerValue := doc["on"]
	workflow.Events = extractGitHubWorkflowEvents(triggerValue)
	workflow.UsesPullRequestTarget = containsString(workflow.Events, "pull_request_target")
	workflow.UsesRepositoryDispatch = containsString(workflow.Events, "repository_dispatch")
	workflow.UsesWorkflowDispatch = containsString(workflow.Events, "workflow_dispatch")
	workflow.UsesSchedule = containsString(workflow.Events, "schedule")
	workflow.Permissions = extractGitHubWorkflowPermissions(doc["permissions"])
	return workflow
}

func extractGitHubWorkflowEvents(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		return []string{strings.TrimSpace(typed)}
	case []interface{}:
		events := make([]string, 0, len(typed))
		for _, item := range typed {
			if event, ok := item.(string); ok && strings.TrimSpace(event) != "" {
				events = append(events, strings.TrimSpace(event))
			}
		}
		sort.Strings(events)
		return uniqueStrings(events)
	case map[string]interface{}:
		events := make([]string, 0, len(typed))
		for key := range typed {
			events = append(events, strings.TrimSpace(key))
		}
		sort.Strings(events)
		return uniqueStrings(events)
	default:
		return nil
	}
}

func extractGitHubWorkflowPermissions(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", key, typed[key]))
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func containsString(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func (c *Client) getWorkflowInfo(ctx context.Context) (string, error) {
	workflows, _, err := c.client.Actions.ListWorkflows(ctx, c.owner, c.repo, nil)
	if err != nil {
		return "", err
	}
	if len(workflows.Workflows) == 0 {
		return "No GitHub Actions workflows found.\n", nil
	}

	var info strings.Builder
	for _, workflow := range workflows.Workflows {
		info.WriteString(fmt.Sprintf("- Workflow: %s", workflow.GetName()))
		info.WriteString(fmt.Sprintf(", State: %s", workflow.GetState()))
		if workflow.GetBadgeURL() != "" {
			info.WriteString(fmt.Sprintf(", Badge: %s", workflow.GetBadgeURL()))
		}
		info.WriteString("\n")
	}

	return info.String(), nil
}

func (c *Client) getWorkflowRunsInfo(ctx context.Context) (string, error) {
	runs, err := c.listWorkflowRuns(ctx, 10)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "No recent workflow runs found.\n", nil
	}

	var info strings.Builder
	for _, run := range runs {
		info.WriteString(fmt.Sprintf("- Run #%d: %s", run.RunNumber, formatWorkflowRunLabel(run)))
		info.WriteString(fmt.Sprintf(", Status: %s", run.Status))
		info.WriteString(fmt.Sprintf(", Conclusion: %s", run.Conclusion))
		info.WriteString(fmt.Sprintf(", Branch: %s", run.HeadBranch))
		if !run.CreatedAt.IsZero() {
			info.WriteString(fmt.Sprintf(", Created: %s", run.CreatedAt.Format(time.RFC3339)))
		}
		if strings.TrimSpace(run.Actor) != "" {
			info.WriteString(fmt.Sprintf(", Actor: %s", run.Actor))
		}
		if run.HTMLURL != "" {
			info.WriteString(fmt.Sprintf(", URL: %s", run.HTMLURL))
		}
		if run.LogsURL != "" {
			info.WriteString(fmt.Sprintf(", Logs: %s", run.LogsURL))
		}
		info.WriteString("\n")
	}

	return info.String(), nil
}

func (c *Client) listWorkflowRuns(ctx context.Context, perPage int) ([]workflowRunSummary, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return nil, err
	}
	if perPage <= 0 {
		perPage = 10
	}

	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/actions/runs?per_page=%d", owner, repo, perPage))
	if err != nil {
		return nil, err
	}

	var raw struct {
		WorkflowRuns []struct {
			ID           int64     `json:"id"`
			RunNumber    int       `json:"run_number"`
			Name         string    `json:"name"`
			DisplayTitle string    `json:"display_title"`
			Status       string    `json:"status"`
			Conclusion   string    `json:"conclusion"`
			HeadBranch   string    `json:"head_branch"`
			Event        string    `json:"event"`
			HTMLURL      string    `json:"html_url"`
			LogsURL      string    `json:"logs_url"`
			ArtifactsURL string    `json:"artifacts_url"`
			CreatedAt    time.Time `json:"created_at"`
			UpdatedAt    time.Time `json:"updated_at"`
			Actor        struct {
				Login string `json:"login"`
			} `json:"actor"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub workflow runs: %w", err)
	}

	runs := make([]workflowRunSummary, 0, len(raw.WorkflowRuns))
	for _, item := range raw.WorkflowRuns {
		runs = append(runs, workflowRunSummary{
			ID:           item.ID,
			RunNumber:    item.RunNumber,
			Name:         strings.TrimSpace(item.Name),
			DisplayTitle: strings.TrimSpace(item.DisplayTitle),
			Status:       strings.TrimSpace(item.Status),
			Conclusion:   strings.TrimSpace(item.Conclusion),
			HeadBranch:   strings.TrimSpace(item.HeadBranch),
			Event:        strings.TrimSpace(item.Event),
			HTMLURL:      strings.TrimSpace(item.HTMLURL),
			LogsURL:      strings.TrimSpace(item.LogsURL),
			ArtifactsURL: strings.TrimSpace(item.ArtifactsURL),
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
			Actor:        strings.TrimSpace(item.Actor.Login),
		})
	}
	return runs, nil
}

func (c *Client) listWorkflowJobs(ctx context.Context, runID int64) ([]workflowJobSummary, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return nil, err
	}

	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/actions/runs/%d/jobs?per_page=100", owner, repo, runID))
	if err != nil {
		return nil, err
	}

	var raw struct {
		Jobs []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion"`
			HTMLURL     string `json:"html_url"`
			CheckRunURL string `json:"check_run_url"`
			Steps       []struct {
				Number     int    `json:"number"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub workflow jobs: %w", err)
	}

	jobs := make([]workflowJobSummary, 0, len(raw.Jobs))
	for _, item := range raw.Jobs {
		steps := make([]workflowJobStepSummary, 0, len(item.Steps))
		for _, step := range item.Steps {
			steps = append(steps, workflowJobStepSummary{
				Number:     step.Number,
				Name:       strings.TrimSpace(step.Name),
				Status:     strings.TrimSpace(step.Status),
				Conclusion: strings.TrimSpace(step.Conclusion),
			})
		}
		jobs = append(jobs, workflowJobSummary{
			ID:          item.ID,
			Name:        strings.TrimSpace(item.Name),
			Status:      strings.TrimSpace(item.Status),
			Conclusion:  strings.TrimSpace(item.Conclusion),
			HTMLURL:     strings.TrimSpace(item.HTMLURL),
			CheckRunURL: strings.TrimSpace(item.CheckRunURL),
			Steps:       steps,
		})
	}
	return jobs, nil
}

func (c *Client) listWorkflowRunArtifacts(ctx context.Context, runID int64) ([]workflowArtifactSummary, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return nil, err
	}

	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/actions/runs/%d/artifacts?per_page=100", owner, repo, runID))
	if err != nil {
		return nil, err
	}

	var raw struct {
		Artifacts []struct {
			ID                 int64     `json:"id"`
			Name               string    `json:"name"`
			SizeInBytes        int64     `json:"size_in_bytes"`
			Expired            bool      `json:"expired"`
			CreatedAt          time.Time `json:"created_at"`
			ExpiresAt          time.Time `json:"expires_at"`
			ArchiveDownloadURL string    `json:"archive_download_url"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub workflow artifacts: %w", err)
	}

	artifacts := make([]workflowArtifactSummary, 0, len(raw.Artifacts))
	for _, item := range raw.Artifacts {
		artifacts = append(artifacts, workflowArtifactSummary{
			ID:                 item.ID,
			Name:               strings.TrimSpace(item.Name),
			SizeInBytes:        item.SizeInBytes,
			Expired:            item.Expired,
			CreatedAt:          item.CreatedAt,
			ExpiresAt:          item.ExpiresAt,
			ArchiveDownloadURL: strings.TrimSpace(item.ArchiveDownloadURL),
		})
	}
	return artifacts, nil
}

func (c *Client) listCheckRunAnnotations(ctx context.Context, checkRunURL string, limit int) ([]workflowAnnotationSummary, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return nil, err
	}
	checkRunID := parseCheckRunID(checkRunURL)
	if checkRunID == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	output, err := runGitHubCLIWithRetry(ctx, "api", fmt.Sprintf("repos/%s/%s/check-runs/%d/annotations?per_page=%d", owner, repo, checkRunID, limit))
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Path            string `json:"path"`
		StartLine       int    `json:"start_line"`
		EndLine         int    `json:"end_line"`
		AnnotationLevel string `json:"annotation_level"`
		Message         string `json:"message"`
		Title           string `json:"title"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub workflow annotations: %w", err)
	}

	annotations := make([]workflowAnnotationSummary, 0, len(raw))
	for _, item := range raw {
		annotations = append(annotations, workflowAnnotationSummary{
			Path:            strings.TrimSpace(item.Path),
			StartLine:       item.StartLine,
			EndLine:         item.EndLine,
			AnnotationLevel: strings.TrimSpace(item.AnnotationLevel),
			Message:         strings.TrimSpace(item.Message),
			Title:           strings.TrimSpace(item.Title),
		})
	}
	return annotations, nil
}

func (c *Client) getWorkflowRunDetailsInfo(ctx context.Context) (string, error) {
	runs, err := c.listWorkflowRuns(ctx, 8)
	if err != nil {
		return "", err
	}
	selectedRuns := selectWorkflowRunsForDetail(runs, 3)
	if len(selectedRuns) == 0 {
		return "No detailed workflow run data found.\n", nil
	}

	var info strings.Builder
	for _, run := range selectedRuns {
		info.WriteString(fmt.Sprintf("- Run #%d: %s", run.RunNumber, formatWorkflowRunLabel(run)))
		info.WriteString(fmt.Sprintf(", status=%s", run.Status))
		if strings.TrimSpace(run.Conclusion) != "" {
			info.WriteString(fmt.Sprintf(", conclusion=%s", run.Conclusion))
		}
		if strings.TrimSpace(run.HeadBranch) != "" {
			info.WriteString(fmt.Sprintf(", branch=%s", run.HeadBranch))
		}
		if strings.TrimSpace(run.Event) != "" {
			info.WriteString(fmt.Sprintf(", event=%s", run.Event))
		}
		if strings.TrimSpace(run.Actor) != "" {
			info.WriteString(fmt.Sprintf(", actor=%s", run.Actor))
		}
		if !run.CreatedAt.IsZero() {
			info.WriteString(fmt.Sprintf(", created=%s", run.CreatedAt.Format(time.RFC3339)))
		}
		if run.HTMLURL != "" {
			info.WriteString(fmt.Sprintf(", url=%s", run.HTMLURL))
		}
		if run.LogsURL != "" {
			info.WriteString(fmt.Sprintf(", logs=%s", run.LogsURL))
		}
		info.WriteString("\n")

		jobs, err := c.listWorkflowJobs(ctx, run.ID)
		if err != nil {
			return "", err
		}
		if len(jobs) == 0 {
			info.WriteString("  jobs: none reported\n")
		} else {
			jobLimit := len(jobs)
			if jobLimit > 6 {
				jobLimit = 6
			}
			for i := 0; i < jobLimit; i++ {
				job := jobs[i]
				info.WriteString(fmt.Sprintf("  - Job: %s, status=%s, conclusion=%s", job.Name, job.Status, job.Conclusion))
				if job.HTMLURL != "" {
					info.WriteString(fmt.Sprintf(", url=%s", job.HTMLURL))
				}
				info.WriteString("\n")

				failedSteps := failedWorkflowJobSteps(job.Steps)
				for _, step := range failedSteps {
					info.WriteString(fmt.Sprintf("    failed step: #%d %s, status=%s, conclusion=%s\n", step.Number, step.Name, step.Status, step.Conclusion))
				}

				if workflowJobNeedsAttention(job) {
					annotations, err := c.listCheckRunAnnotations(ctx, job.CheckRunURL, 3)
					if err == nil {
						for _, annotation := range annotations {
							location := strings.TrimSpace(annotation.Path)
							if location != "" && annotation.StartLine > 0 {
								location = fmt.Sprintf("%s:%d", location, annotation.StartLine)
							}
							if annotation.EndLine > annotation.StartLine && location != "" {
								location = fmt.Sprintf("%s-%d", location, annotation.EndLine)
							}
							info.WriteString(fmt.Sprintf("    annotation: level=%s", annotation.AnnotationLevel))
							if strings.TrimSpace(annotation.Title) != "" {
								info.WriteString(fmt.Sprintf(", title=%s", annotation.Title))
							}
							if location != "" {
								info.WriteString(fmt.Sprintf(", location=%s", location))
							}
							if strings.TrimSpace(annotation.Message) != "" {
								info.WriteString(fmt.Sprintf(", message=%s", annotation.Message))
							}
							info.WriteString("\n")
						}
					}
				}
			}
			if len(jobs) > jobLimit {
				info.WriteString(fmt.Sprintf("  - ... %d additional jobs omitted\n", len(jobs)-jobLimit))
			}
		}

		artifacts, err := c.listWorkflowRunArtifacts(ctx, run.ID)
		if err != nil {
			return "", err
		}
		if len(artifacts) > 0 {
			artifactLimit := len(artifacts)
			if artifactLimit > 5 {
				artifactLimit = 5
			}
			for i := 0; i < artifactLimit; i++ {
				artifact := artifacts[i]
				info.WriteString(fmt.Sprintf("  - Artifact: %s, sizeBytes=%d, expired=%t", artifact.Name, artifact.SizeInBytes, artifact.Expired))
				if !artifact.ExpiresAt.IsZero() {
					info.WriteString(fmt.Sprintf(", expires=%s", artifact.ExpiresAt.Format(time.RFC3339)))
				}
				if artifact.ArchiveDownloadURL != "" {
					info.WriteString(fmt.Sprintf(", download=%s", artifact.ArchiveDownloadURL))
				}
				info.WriteString("\n")
			}
			if len(artifacts) > artifactLimit {
				info.WriteString(fmt.Sprintf("  - ... %d additional artifacts omitted\n", len(artifacts)-artifactLimit))
			}
		}
	}

	return info.String(), nil
}

func (c *Client) getPullRequestInfo(ctx context.Context) (string, error) {
	prs, _, err := c.client.PullRequests.List(ctx, c.owner, c.repo, &github.PullRequestListOptions{State: "all", ListOptions: github.ListOptions{PerPage: 5}})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, pr := range prs {
		info.WriteString(fmt.Sprintf("- PR #%d: %s", pr.GetNumber(), pr.GetTitle()))
		info.WriteString(fmt.Sprintf(", State: %s", pr.GetState()))
		info.WriteString(fmt.Sprintf(", Author: %s", pr.GetUser().GetLogin()))
		if pr.CreatedAt != nil {
			info.WriteString(fmt.Sprintf(", Created: %s", pr.CreatedAt.Format("2006-01-02")))
		}
		if pr.GetHTMLURL() != "" {
			info.WriteString(fmt.Sprintf(", URL: %s", pr.GetHTMLURL()))
		}
		info.WriteString("\n")
	}

	return info.String(), nil
}

func (c *Client) GetWorkflowStatus(ctx context.Context, workflowName string) (string, error) {
	if _, _, err := c.ResolveRepository(ctx); err != nil {
		return "", err
	}

	workflows, _, err := c.client.Actions.ListWorkflows(ctx, c.owner, c.repo, nil)
	if err != nil {
		return "", err
	}

	for _, workflow := range workflows.Workflows {
		if workflow.GetName() == workflowName {
			runs, _, err := c.client.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflow.GetID(), &github.ListWorkflowRunsOptions{ListOptions: github.ListOptions{PerPage: 1}})
			if err != nil {
				return "", err
			}

			if len(runs.WorkflowRuns) > 0 {
				run := runs.WorkflowRuns[0]
				return fmt.Sprintf("Workflow '%s' - Status: %s, Conclusion: %s, Run #%d", workflowName, run.GetStatus(), run.GetConclusion(), run.GetRunNumber()), nil
			}

			return fmt.Sprintf("Workflow '%s' found but no runs available", workflowName), nil
		}
	}

	return fmt.Sprintf("Workflow '%s' not found", workflowName), nil
}
