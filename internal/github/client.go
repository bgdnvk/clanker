package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
)

type Client struct {
	client *github.Client
	owner  string
	repo   string
}

func NewClient(token, owner, repo string) *Client {
	var client *github.Client

	if token != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: token},
		)
		tc := oauth2.NewClient(context.Background(), ts)
		client = github.NewClient(tc)
	} else {
		client = github.NewClient(nil)
	}

	return &Client{
		client: client,
		owner:  owner,
		repo:   repo,
	}
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	var context strings.Builder

	questionLower := strings.ToLower(question)

	if strings.Contains(questionLower, "action") || strings.Contains(questionLower, "workflow") || strings.Contains(questionLower, "ci") || strings.Contains(questionLower, "build") {
		workflowInfo, err := c.getWorkflowInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow info: %w", err)
		}
		context.WriteString("GitHub Actions Workflows:\n")
		context.WriteString(workflowInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "run") || strings.Contains(questionLower, "execution") || strings.Contains(questionLower, "status") {
		runsInfo, err := c.getWorkflowRunsInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get workflow runs info: %w", err)
		}
		context.WriteString("Recent Workflow Runs:\n")
		context.WriteString(runsInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "pull") || strings.Contains(questionLower, "pr") {
		prInfo, err := c.getPullRequestInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get pull request info: %w", err)
		}
		context.WriteString("Recent Pull Requests:\n")
		context.WriteString(prInfo)
		context.WriteString("\n\n")
	}

	return context.String(), nil
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
	opts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	}

	runs, _, err := c.client.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, opts)
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
	opts := &github.PullRequestListOptions{
		State:       "all",
		ListOptions: github.ListOptions{PerPage: 5},
	}

	prs, _, err := c.client.PullRequests.List(ctx, c.owner, c.repo, opts)
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

// GetWorkflowStatus returns the status of a specific workflow
func (c *Client) GetWorkflowStatus(ctx context.Context, workflowName string) (string, error) {
	workflows, _, err := c.client.Actions.ListWorkflows(ctx, c.owner, c.repo, nil)
	if err != nil {
		return "", err
	}

	for _, workflow := range workflows.Workflows {
		if workflow.GetName() == workflowName {
			runs, _, err := c.client.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflow.GetID(), &github.ListWorkflowRunsOptions{
				ListOptions: github.ListOptions{PerPage: 1},
			})
			if err != nil {
				return "", err
			}

			if len(runs.WorkflowRuns) > 0 {
				run := runs.WorkflowRuns[0]
				return fmt.Sprintf("Workflow '%s' - Status: %s, Conclusion: %s, Run #%d",
					workflowName, run.GetStatus(), run.GetConclusion(), run.GetRunNumber()), nil
			}

			return fmt.Sprintf("Workflow '%s' found but no runs available", workflowName), nil
		}
	}

	return fmt.Sprintf("Workflow '%s' not found", workflowName), nil
}
