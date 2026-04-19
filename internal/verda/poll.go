package verda

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PollOptions tune the bounded polling helpers.
type PollOptions struct {
	Interval time.Duration
	Max      time.Duration
}

func (o PollOptions) withDefaults(interval, max time.Duration) PollOptions {
	if o.Interval <= 0 {
		o.Interval = interval
	}
	if o.Max <= 0 {
		o.Max = max
	}
	return o
}

// WaitInstanceRunning polls GET /v1/instances/{id} until the instance reports
// `running` or a terminal status, or the bounded deadline is reached.
func (c *Client) WaitInstanceRunning(ctx context.Context, id string, opts PollOptions) (*Instance, error) {
	opts = opts.withDefaults(15*time.Second, 15*time.Minute)
	deadline := time.Now().Add(opts.Max)

	for {
		body, err := c.RunAPIWithContext(ctx, http.MethodGet, "/v1/instances/"+id, "")
		if err != nil {
			return nil, err
		}
		var inst Instance
		if err := json.Unmarshal([]byte(body), &inst); err != nil {
			return nil, fmt.Errorf("decode instance: %w", err)
		}

		if isTerminalInstanceStatus(inst.Status) {
			return &inst, nil
		}

		if time.Now().After(deadline) {
			return &inst, fmt.Errorf("timeout waiting for instance %s to reach running (last status=%s)", id, inst.Status)
		}

		select {
		case <-ctx.Done():
			return &inst, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

// WaitClusterRunning polls GET /v1/clusters/{id} until the cluster reports a
// terminal status (running, error, discontinued, etc.) or the deadline hits.
func (c *Client) WaitClusterRunning(ctx context.Context, id string, opts PollOptions) (*Cluster, error) {
	opts = opts.withDefaults(30*time.Second, 30*time.Minute)
	deadline := time.Now().Add(opts.Max)

	for {
		body, err := c.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters/"+id, "")
		if err != nil {
			return nil, err
		}
		var cl Cluster
		if err := json.Unmarshal([]byte(body), &cl); err != nil {
			return nil, fmt.Errorf("decode cluster: %w", err)
		}

		if isTerminalClusterStatus(cl.Status) {
			return &cl, nil
		}

		if time.Now().After(deadline) {
			return &cl, fmt.Errorf("timeout waiting for cluster %s to reach running (last status=%s)", id, cl.Status)
		}

		select {
		case <-ctx.Done():
			return &cl, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

// WaitVolumeAvailable polls GET /v1/volumes/{id} until the volume reports a
// stable status (`attached`, `created`, `detached`, `deleted`) or times out.
func (c *Client) WaitVolumeAvailable(ctx context.Context, id string, opts PollOptions) (*Volume, error) {
	opts = opts.withDefaults(10*time.Second, 10*time.Minute)
	deadline := time.Now().Add(opts.Max)

	for {
		body, err := c.RunAPIWithContext(ctx, http.MethodGet, "/v1/volumes/"+id, "")
		if err != nil {
			return nil, err
		}
		var v Volume
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, fmt.Errorf("decode volume: %w", err)
		}

		if isTerminalVolumeStatus(v.Status) {
			return &v, nil
		}

		if time.Now().After(deadline) {
			return &v, fmt.Errorf("timeout waiting for volume %s to reach ready (last status=%s)", id, v.Status)
		}

		select {
		case <-ctx.Done():
			return &v, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

func isTerminalInstanceStatus(s string) bool {
	switch s {
	case StatusRunning, StatusError, StatusDiscontinued, StatusNotFound,
		StatusNoCapacity, StatusInstallationFailed, StatusOffline:
		return true
	}
	return false
}

func isTerminalClusterStatus(s string) bool {
	switch s {
	case StatusRunning, StatusError, StatusDiscontinued, StatusNotFound,
		StatusNoCapacity, StatusInstallationFailed:
		return true
	}
	return false
}

func isTerminalVolumeStatus(s string) bool {
	switch s {
	case "attached", "detached", "created", "deleted", "exported", "canceled":
		return true
	}
	return false
}
