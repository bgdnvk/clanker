package sentry

import (
	"context"
	"fmt"
)

// ListMonitors returns Sentry Crons monitors for an org.
func (c *Client) ListMonitors(ctx context.Context, orgSlug string) ([]Monitor, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/monitors/", org), nil)
	if err != nil {
		return nil, err
	}
	var monitors []Monitor
	if err := DecodeJSON(body, &monitors); err != nil {
		return nil, err
	}
	return monitors, nil
}

// GetMonitor fetches a single monitor by slug.
func (c *Client) GetMonitor(ctx context.Context, orgSlug, monitorSlug string) (*Monitor, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || monitorSlug == "" {
		return nil, fmt.Errorf("org slug and monitor slug are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/monitors/%s/", org, monitorSlug), nil)
	if err != nil {
		return nil, err
	}
	var m Monitor
	if err := DecodeJSON(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMonitorCheckins returns recent check-ins for a monitor.
func (c *Client) GetMonitorCheckins(ctx context.Context, orgSlug, monitorSlug string, limit int) ([]MonitorCheckin, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || monitorSlug == "" {
		return nil, fmt.Errorf("org slug and monitor slug are required")
	}
	params := map[string]string{}
	if limit > 0 {
		params["per_page"] = fmt.Sprintf("%d", limit)
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/monitors/%s/checkins/%s", org, monitorSlug, BuildQuery(params)), nil)
	if err != nil {
		return nil, err
	}
	var checkins []MonitorCheckin
	if err := DecodeJSON(body, &checkins); err != nil {
		return nil, err
	}
	return checkins, nil
}

// MuteMonitor marks a monitor as muted (no alerts on missed check-ins).
func (c *Client) MuteMonitor(ctx context.Context, orgSlug, monitorSlug string) error {
	return c.setMonitorMute(ctx, orgSlug, monitorSlug, true)
}

// UnmuteMonitor restores alerting on a previously muted monitor.
func (c *Client) UnmuteMonitor(ctx context.Context, orgSlug, monitorSlug string) error {
	return c.setMonitorMute(ctx, orgSlug, monitorSlug, false)
}

func (c *Client) setMonitorMute(ctx context.Context, orgSlug, monitorSlug string, muted bool) error {
	org := c.resolveOrg(orgSlug)
	if org == "" || monitorSlug == "" {
		return fmt.Errorf("org slug and monitor slug are required")
	}
	body := map[string]any{"isMuted": muted}
	_, _, err := c.Do(ctx, "PUT", fmt.Sprintf("/organizations/%s/monitors/%s/", org, monitorSlug), body)
	return err
}
