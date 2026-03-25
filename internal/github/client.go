package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
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

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	owner, repo, err := c.ResolveRepository(ctx)
	if err != nil {
		return "", err
	}
	c.owner = owner
	c.repo = repo

	var contextText strings.Builder
	questionLower := strings.ToLower(question)

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

func (c *Client) getWorkflowInfo(ctx context.Context) (string, error) {
	workflows, _, err := c.client.Actions.ListWorkflows(ctx, c.owner, c.repo, nil)
	if err != nil {
		return "", err
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
	runs, _, err := c.client.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, &github.ListWorkflowRunsOptions{ListOptions: github.ListOptions{PerPage: 10}})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, run := range runs.WorkflowRuns {
		info.WriteString(fmt.Sprintf("- Run #%d: %s", run.GetRunNumber(), run.GetDisplayTitle()))
		info.WriteString(fmt.Sprintf(", Status: %s", run.GetStatus()))
		info.WriteString(fmt.Sprintf(", Conclusion: %s", run.GetConclusion()))
		info.WriteString(fmt.Sprintf(", Branch: %s", run.GetHeadBranch()))
		if run.CreatedAt != nil {
			info.WriteString(fmt.Sprintf(", Created: %s", run.CreatedAt.Format(time.RFC3339)))
		}
		if run.GetHTMLURL() != "" {
			info.WriteString(fmt.Sprintf(", URL: %s", run.GetHTMLURL()))
		}
		info.WriteString("\n")
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
