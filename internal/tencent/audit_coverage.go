package tencent

import (
	"context"
	"encoding/json"

	cloudaudit "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cloudaudit/v20190319"
)

// AuditLogCoverageScanJSON returns the high-signal answer to "is the
// account logging API calls at all?" Cloud Audit is account-global so this
// audit takes no region parameter. Posture:
//   - NO_TRACKS   : no audit tracks configured (no API-call audit trail)
//   - ALL_DISABLED: tracks exist but all are disabled
//   - PARTIAL     : some enabled, some not
//   - FULL        : every track is enabled
func (c *Client) AuditLogCoverageScanJSON(ctx context.Context) (string, error) {
	client, err := newCloudAuditClient(c)
	if err != nil {
		return "", err
	}
	resp, err := client.ListAudits(cloudaudit.NewListAuditsRequest())
	if err != nil {
		return "", friendlyError(err)
	}

	type trackRow struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		COSBucket string `json:"cos_bucket,omitempty"`
		Prefix    string `json:"log_prefix,omitempty"`
	}
	var tracks []trackRow
	enabledCount, disabledCount := 0, 0
	if resp != nil && resp.Response != nil {
		for _, a := range resp.Response.AuditSummarys {
			en := derefInt64Raw(a.AuditStatus) == 1
			if en {
				enabledCount++
			} else {
				disabledCount++
			}
			tracks = append(tracks, trackRow{
				Name:      derefStringRaw(a.AuditName),
				Enabled:   en,
				COSBucket: derefStringRaw(a.CosBucketName),
				Prefix:    derefStringRaw(a.LogFilePrefix),
			})
		}
	}
	posture := "NO_TRACKS"
	if len(tracks) > 0 {
		switch {
		case enabledCount > 0 && disabledCount == 0:
			posture = "FULL"
		case enabledCount > 0 && disabledCount > 0:
			posture = "PARTIAL"
		default:
			posture = "ALL_DISABLED"
		}
	}
	out := struct {
		Posture       string     `json:"posture"`
		EnabledCount  int        `json:"enabled_count"`
		DisabledCount int        `json:"disabled_count"`
		Tracks        []trackRow `json:"tracks"`
	}{posture, enabledCount, disabledCount, tracks}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
