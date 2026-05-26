package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ExpiryItem is one PREPAID resource and its renewal deadline. Built by
// ExpiryReport from the slim JSON the existing context*() gatherers emit
// so the cron CLI / HTTP / MCP surfaces all see the same shape.
type ExpiryItem struct {
	Region      string `json:"region"`
	Type        string `json:"type"` // cvm, lighthouse, cbs, mysql, ...
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	ExpiresAt   string `json:"expires_at"` // RFC3339 from the SDK
	DaysLeft    int    `json:"days_left"`  // negative when already expired
	AutoRenew   *bool  `json:"auto_renew,omitempty"`
	BillingMode string `json:"billing_mode,omitempty"`
	State       string `json:"state,omitempty"`
}

// ExpiryReport is the cron-facing rollup. counts.expired drives exit code 2,
// counts.flagged drives exit code 1, everything else is exit code 0.
type ExpiryReport struct {
	GeneratedAt   string       `json:"generated_at"`
	ThresholdDays int          `json:"threshold_days"`
	ManualOnly    bool         `json:"manual_only"`
	Regions       []string     `json:"regions"`
	Items         []ExpiryItem `json:"items"`
	Counts        ExpiryCounts `json:"counts"`
}

// ExpiryCounts breaks the report down so callers don't have to re-scan items
// to know severity. Total includes everything PREPAID; Flagged is anything at
// or under the threshold; Expired is anything past expires_at; AutoRenew is
// the slice of Flagged that has auto_renew=true (these are dropped from
// Items when ManualOnly is set).
type ExpiryCounts struct {
	Total     int `json:"total"`
	Flagged   int `json:"flagged"`
	Expired   int `json:"expired"`
	AutoRenew int `json:"auto_renew"`
}

// expiryResourceTypes are the PREPAID-capable types we sweep. Order is the
// CLI table order. SSL is excluded by default — cert validity is a different
// signal from subscription expiry — but BuildExpiryReport accepts a flag to
// fold it in for callers that want to monitor cert renewal too.
var expiryResourceTypes = []string{
	"lighthouse",
	"cvm",
	"cbs",
	"mysql",
	"postgres",
	"redis",
	"mongodb",
	"cynosdb",
	"clb",
	"antiddos",
}

// ExpiryReportOptions controls a BuildExpiryReport call. ThresholdDays of
// zero defaults to 30 — most renewal-window emails arrive 30 days out.
// ManualOnly defaults to true since the cron is asking "what won't auto-
// renew?" — items with auto_renew=true are pre-filtered from Items but
// still counted in ExpiryCounts.AutoRenew.
type ExpiryReportOptions struct {
	Regions       []string
	ThresholdDays int
	ManualOnly    bool
	IncludeSSL    bool
}

// BuildExpiryReport walks every PREPAID-capable resource across the
// requested regions and rolls them up by days-to-expiry. Empty Regions
// means "use the client's configured region only".
func (c *Client) BuildExpiryReport(ctx context.Context, opt ExpiryReportOptions) (*ExpiryReport, error) {
	if opt.ThresholdDays <= 0 {
		opt.ThresholdDays = 30
	}
	regions := opt.Regions
	if len(regions) == 0 {
		regions = []string{c.creds.Region}
	}

	report := &ExpiryReport{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		ThresholdDays: opt.ThresholdDays,
		ManualOnly:    opt.ManualOnly,
		Regions:       regions,
	}

	now := time.Now().UTC()
	for _, region := range regions {
		if err := ctxDone(ctx); err != nil {
			return nil, err
		}
		rc := c.WithRegion(region)
		for _, rt := range expiryResourceTypes {
			items, err := gatherExpiryByType(ctx, rc, rt)
			if err != nil {
				// One service failing (e.g., not enabled in this region)
				// shouldn't poison the whole report.
				continue
			}
			for _, it := range items {
				if it.BillingMode != billingPrepaid {
					continue
				}
				it.Region = region
				it.Type = rt
				it.DaysLeft = computeDaysLeft(now, it.ExpiresAt)
				report.Counts.Total++
				if it.DaysLeft < 0 {
					report.Counts.Expired++
				}
				flagged := it.DaysLeft <= opt.ThresholdDays
				if flagged {
					report.Counts.Flagged++
					if it.AutoRenew != nil && *it.AutoRenew {
						report.Counts.AutoRenew++
						if opt.ManualOnly {
							continue
						}
					}
					report.Items = append(report.Items, it)
				}
			}
		}
		if opt.IncludeSSL {
			items, err := gatherSSLExpiry(ctx, rc)
			if err == nil {
				for _, it := range items {
					it.Region = region
					it.Type = "ssl"
					report.Counts.Total++
					if it.DaysLeft < 0 {
						report.Counts.Expired++
					}
					if it.DaysLeft <= opt.ThresholdDays {
						report.Counts.Flagged++
						report.Items = append(report.Items, it)
					}
				}
			}
		}
	}

	sort.SliceStable(report.Items, func(i, j int) bool {
		return report.Items[i].DaysLeft < report.Items[j].DaysLeft
	})
	return report, nil
}

// gatherExpiryByType calls the matching JSON*() function and parses out the
// fields ExpiryReport needs. We don't import the typed SDK structs here —
// the slim JSON shape (id, name, state, expires_at, billing_mode,
// auto_renew) is the contract these functions already emit after the
// upstream review work.
func gatherExpiryByType(ctx context.Context, c *Client, resourceType string) ([]ExpiryItem, error) {
	var body string
	var err error
	switch resourceType {
	case "cvm":
		body, err = c.JSONCVMs(ctx)
	case "lighthouse":
		body, err = c.JSONLighthouses(ctx)
	case "cbs":
		body, err = c.JSONCBS(ctx)
	case "mysql":
		body, err = c.JSONMySQL(ctx)
	case "postgres":
		body, err = c.JSONPostgres(ctx)
	case "redis":
		body, err = c.JSONRedis(ctx)
	case "mongodb":
		body, err = c.JSONMongoDB(ctx)
	case "cynosdb":
		body, err = c.JSONCynosDB(ctx)
	case "clb":
		body, err = c.JSONCLB(ctx)
	case "antiddos":
		body, err = c.JSONAntiDDoS(ctx)
	default:
		return nil, fmt.Errorf("unsupported expiry resource type %q", resourceType)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var items []ExpiryItem
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		return nil, fmt.Errorf("parse %s slim json: %w", resourceType, err)
	}
	return items, nil
}

// sslExpiry is the slim cert shape JSONSSL emits — different from the rest
// of the resources because SSL doesn't have a billing_mode/expires_at pair;
// it has cert_end and a pre-computed days_left.
type sslExpiry struct {
	ID       string `json:"id"`
	Alias    string `json:"alias,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Status   string `json:"status"`
	CertEnd  string `json:"cert_end,omitempty"`
	DaysLeft int    `json:"days_left"`
}

func gatherSSLExpiry(ctx context.Context, c *Client) ([]ExpiryItem, error) {
	body, err := c.JSONSSL(ctx)
	if err != nil || strings.TrimSpace(body) == "" {
		return nil, err
	}
	var raw []sslExpiry
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("parse ssl slim json: %w", err)
	}
	out := make([]ExpiryItem, 0, len(raw))
	for _, r := range raw {
		name := r.Alias
		if name == "" {
			name = r.Domain
		}
		out = append(out, ExpiryItem{
			ID:          r.ID,
			Name:        name,
			ExpiresAt:   r.CertEnd,
			DaysLeft:    r.DaysLeft,
			BillingMode: billingPrepaid,
			State:       r.Status,
		})
	}
	return out, nil
}

// computeDaysLeft accepts the RFC3339 timestamps the Tencent SDK emits and
// returns the integer days remaining. Negative means already expired. We
// floor (rather than round) so "expires in 2 hours" reads as 0 days left
// rather than 1 — that's the conservative direction for a renewal alert.
func computeDaysLeft(now time.Time, expiresAt string) int {
	if strings.TrimSpace(expiresAt) == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, expiresAt); err == nil {
			delta := t.UTC().Sub(now)
			return int(delta / (24 * time.Hour))
		}
	}
	return 0
}
