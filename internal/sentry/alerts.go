package sentry

import (
	"context"
	"fmt"
)

// ListIssueAlertRules returns project-scoped issue alert rules (the legacy
// /rules/ endpoint). For metric alerts, use ListMetricAlertRules.
func (c *Client) ListIssueAlertRules(ctx context.Context, orgSlug, projectSlug string) ([]IssueAlertRule, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/rules/", org, projectSlug), nil)
	if err != nil {
		return nil, err
	}
	var rules []IssueAlertRule
	if err := DecodeJSON(body, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// ListMetricAlertRules returns org-scoped metric alert rules.
func (c *Client) ListMetricAlertRules(ctx context.Context, orgSlug string) ([]MetricAlertRule, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/alert-rules/", org), nil)
	if err != nil {
		return nil, err
	}
	var rules []MetricAlertRule
	if err := DecodeJSON(body, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// CreateIssueAlertRule posts a new issue alert rule. The body is passed
// through as-is — the upstream schema for conditions/filters/actions is large
// enough that we don't model it strictly, leaving callers free to construct
// the payload from documentation.
func (c *Client) CreateIssueAlertRule(ctx context.Context, orgSlug, projectSlug string, rule any) (*IssueAlertRule, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	_, body, err := c.Do(ctx, "POST", fmt.Sprintf("/projects/%s/%s/rules/", org, projectSlug), rule)
	if err != nil {
		return nil, err
	}
	var created IssueAlertRule
	if err := DecodeJSON(body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateIssueAlertRule replaces an existing issue alert rule.
func (c *Client) UpdateIssueAlertRule(ctx context.Context, orgSlug, projectSlug, ruleID string, rule any) (*IssueAlertRule, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" || ruleID == "" {
		return nil, fmt.Errorf("org, project slug, and rule ID are required")
	}
	_, body, err := c.Do(ctx, "PUT", fmt.Sprintf("/projects/%s/%s/rules/%s/", org, projectSlug, ruleID), rule)
	if err != nil {
		return nil, err
	}
	var updated IssueAlertRule
	if err := DecodeJSON(body, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// DeleteIssueAlertRule removes an issue alert rule.
func (c *Client) DeleteIssueAlertRule(ctx context.Context, orgSlug, projectSlug, ruleID string) error {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" || ruleID == "" {
		return fmt.Errorf("org, project slug, and rule ID are required")
	}
	_, _, err := c.Do(ctx, "DELETE", fmt.Sprintf("/projects/%s/%s/rules/%s/", org, projectSlug, ruleID), nil)
	return err
}
