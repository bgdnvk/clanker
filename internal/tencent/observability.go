package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	cloudaudit "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cloudaudit/v20190319"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	monitor "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/monitor/v20180724"
)

// listAlarmPolicies prints every Cloud Monitor alarm policy in the region.
// Each policy has a list of trigger conditions; we show count + enable state.
func listAlarmPolicies(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		p      *monitor.AlarmPolicy
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newMonitorClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init monitor client: %v", r, err))
			continue
		}
		req := monitor.NewDescribeAlarmPoliciesRequest()
		module := "monitor"
		req.Module = &module
		var page, pageSize int64 = 1, 100
		req.PageNumber = &page
		req.PageSize = &pageSize
		resp, err := client.DescribeAlarmPolicies(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, p := range resp.Response.Policies {
			rows = append(rows, row{region: r, p: p})
		}
	}

	header := fmt.Sprintf("Cloud Monitor Alarm Policies (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Cloud Monitor Alarm Policies (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No alarm policies found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tPOLICY_ID\tNAME\tENABLED\tTYPE\tBOUND_INSTANCES")
	} else {
		fmt.Fprintln(tw, "POLICY_ID\tNAME\tENABLED\tTYPE\tBOUND_INSTANCES")
	}
	for _, r := range rows {
		p := r.p
		enabled := derefInt64(p.Enable) == 1
		fields := []string{
			derefString(p.PolicyId),
			derefString(p.PolicyName),
			fmt.Sprintf("%v", enabled),
			derefString(p.MonitorType),
			fmt.Sprintf("%d", derefInt64(p.UseSum)),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

func newMonitorClient(c *Client, region string) (*monitor.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("monitor.tencentcloudapi.com")
	return monitor.NewClient(cred, region, cpf)
}

// listCLSTopics prints every CLS log topic in the region.
func listCLSTopics(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		t      *cls.TopicInfo
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newCLSClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init cls client: %v", r, err))
			continue
		}
		resp, err := client.DescribeTopics(cls.NewDescribeTopicsRequest())
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, t := range resp.Response.Topics {
			rows = append(rows, row{region: r, t: t})
		}
	}

	header := fmt.Sprintf("CLS Log Topics (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("CLS Log Topics (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No log topics found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tTOPIC_ID\tNAME\tLOGSET_ID\tPARTITIONS\tINDEX\tCREATED")
	} else {
		fmt.Fprintln(tw, "TOPIC_ID\tNAME\tLOGSET_ID\tPARTITIONS\tINDEX\tCREATED")
	}
	for _, r := range rows {
		t := r.t
		fields := []string{
			derefString(t.TopicId),
			derefString(t.TopicName),
			derefString(t.LogsetId),
			fmt.Sprintf("%d", derefInt64(t.PartitionCount)),
			fmt.Sprintf("%v", derefBool(t.Index)),
			derefString(t.CreateTime),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

// ── CLS log search (Cloud Log Service content) ──────────────────────────────

// CLSLogEntry is one flattened SearchLog result for the console log viewer.
type CLSLogEntry struct {
	Time     int64  `json:"time"` // epoch milliseconds
	Source   string `json:"source"`
	FileName string `json:"filename"`
	HostName string `json:"hostname"`
	Content  string `json:"content"` // structured LogJson, or RawLog fallback
}

// CLSSearchResult is the JSON payload returned to the API for a topic search.
type CLSSearchResult struct {
	Region   string        `json:"region"`
	TopicID  string        `json:"topic_id"`
	Query    string        `json:"query"`
	From     int64         `json:"from"`
	To       int64         `json:"to"`
	ListOver bool          `json:"list_over"`
	Count    int           `json:"count"`
	Results  []CLSLogEntry `json:"results"`
}

// CLSSearchLogJSON runs a CLS SearchLog against a single topic and returns a
// flattened JSON string ({region, topic_id, results:[...]}). Time range is in
// epoch milliseconds; from/to<=0 default to the last hour. An empty query
// matches all logs ("*"). limit is clamped to [1,1000], default 200.
func (c *Client) CLSSearchLogJSON(_ context.Context, region, topicID, query string, fromMs, toMs, limit int64) (string, error) {
	if strings.TrimSpace(topicID) == "" {
		return "", fmt.Errorf("topic_id is required")
	}
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	client, err := newCLSClient(c, region)
	if err != nil {
		return "", fmt.Errorf("init cls client: %w", err)
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if toMs <= 0 {
		toMs = time.Now().UnixMilli()
	}
	if fromMs <= 0 {
		fromMs = toMs - 3600*1000 // last hour
	}
	q := strings.TrimSpace(query)
	if q == "" {
		q = "*"
	}
	sort := "desc"

	req := cls.NewSearchLogRequest()
	req.TopicId = &topicID
	req.From = &fromMs
	req.To = &toMs
	req.Query = &q
	req.Sort = &sort
	req.Limit = &limit

	resp, err := client.SearchLog(req)
	if err != nil {
		return "", friendlyError(err)
	}

	out := CLSSearchResult{Region: region, TopicID: topicID, Query: query, From: fromMs, To: toMs, Results: []CLSLogEntry{}}
	if resp != nil && resp.Response != nil {
		out.ListOver = derefBool(resp.Response.ListOver)
		for _, r := range resp.Response.Results {
			if r == nil {
				continue
			}
			content := derefString(r.LogJson)
			if content == "" {
				content = derefString(r.RawLog)
			}
			out.Results = append(out.Results, CLSLogEntry{
				Time:     derefInt64(r.Time),
				Source:   derefString(r.Source),
				FileName: derefString(r.FileName),
				HostName: derefString(r.HostName),
				Content:  content,
			})
		}
	}
	out.Count = len(out.Results)

	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func newCLSClient(c *Client, region string) (*cls.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("cls.tencentcloudapi.com")
	return cls.NewClient(cred, region, cpf)
}

// listCloudAuditTracks prints every Cloud Audit "track" (API call log).
// Tracks are account-global; the region argument is for the API endpoint.
func listCloudAuditTracks(c *Client) error {
	client, err := newCloudAuditClient(c)
	if err != nil {
		return fmt.Errorf("init cloudaudit client: %w", err)
	}
	resp, err := client.ListAudits(cloudaudit.NewListAuditsRequest())
	if err != nil {
		return fmt.Errorf("ListAudits: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud Audit Tracks:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.AuditSummarys) == 0 {
		fmt.Println("  No Cloud Audit tracks configured")
		fmt.Println()
		fmt.Println("  ⚠️  Without audit tracks, API calls against this account are not logged.")
		fmt.Println("      Enable Cloud Audit + a COS bucket destination to capture who-did-what.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tCOS_BUCKET\tLOG_PREFIX")
	for _, a := range resp.Response.AuditSummarys {
		enabled := derefInt64(a.AuditStatus) == 1
		status := "DISABLED"
		if enabled {
			status = "ENABLED"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			derefString(a.AuditName),
			status,
			derefString(a.CosBucketName),
			derefString(a.LogFilePrefix),
		)
	}
	return tw.Flush()
}

func newCloudAuditClient(c *Client) (*cloudaudit.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("cloudaudit.tencentcloudapi.com")
	return cloudaudit.NewClient(cred, "ap-guangzhou", cpf)
}
