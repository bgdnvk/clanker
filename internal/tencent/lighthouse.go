package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	lighthouse "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/lighthouse/v20200324"
	monitor "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/monitor/v20180724"
)

// Lighthouse returns a region-scoped Tencent Lighthouse SDK client.
// Lighthouse is Tencent's lightweight cloud server (similar to AWS Lightsail);
// it shares the Cloud Monitor metric pipeline as CVM but lives under the
// namespace QCE/LIGHTHOUSE with its own metric names.
func (c *Client) Lighthouse() (*lighthouse.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "lighthouse.tencentcloudapi.com"
	return lighthouse.NewClient(cred, c.creds.Region, cpf)
}

// lighthouseInstance is the slim wire shape returned by JSONLighthouses
// and used as the source list for LighthouseMetricsJSON.
type lighthouseInstance struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	State       string            `json:"state"`
	BundleID    string            `json:"bundle_id,omitempty"`
	BlueprintID string            `json:"blueprint_id,omitempty"`
	Zone        string            `json:"zone,omitempty"`
	PrivateIP   []string          `json:"private_ip,omitempty"`
	PublicIP    []string          `json:"public_ip,omitempty"`
	OSName      string            `json:"os,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	ExpiresAt   string            `json:"expires_at,omitempty"`
	BillingMode string            `json:"billing_mode,omitempty"`
	AutoRenew   *bool             `json:"auto_renew,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// contextLighthouses lists Lighthouse instances in the active region.
// Returns "" when the service has no instances (matches the existing
// contextCVMs convention used by gatherTencentByType).
func (c *Client) contextLighthouses(ctx context.Context) (string, error) {
	cl, err := c.Lighthouse()
	if err != nil {
		return "", err
	}
	req := lighthouse.NewDescribeInstancesRequest()
	resp, err := cl.DescribeInstances(req)
	if err != nil {
		return "", friendlyError(err)
	}
	if resp == nil || resp.Response == nil || len(resp.Response.InstanceSet) == 0 {
		return "", nil
	}
	out := make([]lighthouseInstance, 0, len(resp.Response.InstanceSet))
	for _, in := range resp.Response.InstanceSet {
		row := lighthouseInstance{
			ID:          derefStringRaw(in.InstanceId),
			Name:        derefStringRaw(in.InstanceName),
			State:       derefStringRaw(in.InstanceState),
			BundleID:    derefStringRaw(in.BundleId),
			BlueprintID: derefStringRaw(in.BlueprintId),
			Zone:        derefStringRaw(in.Zone),
			PrivateIP:   stringSlice(in.PrivateAddresses),
			PublicIP:    stringSlice(in.PublicAddresses),
			OSName:      derefStringRaw(in.OsName),
			CreatedAt:   derefStringRaw(in.CreatedTime),
			ExpiresAt:   derefStringRaw(in.ExpiredTime),
			BillingMode: normChargeTypeStr(in.InstanceChargeType),
			AutoRenew:   normRenewFlagAutoStr(in.RenewFlag),
			Tags:        extractTags(in.Tags),
		}
		out = append(out, row)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// JSONLighthouses is the public entrypoint for the /resources/lighthouse route.
func (c *Client) JSONLighthouses(ctx context.Context) (string, error) {
	return c.contextLighthouses(ctx)
}

// LighthouseMetricsJSON pulls one metric (default Cpu_Usage) for every
// Lighthouse instance in the region over the last N minutes (default 60).
// Mirrors CVMMetricsJSON but uses the QCE/LIGHTHOUSE namespace and the
// metric-name convention Tencent uses for Lighthouse (Cpu_Usage, Mem_Usage,
// Public_Bandwidth_In/Out, Internal_Bandwidth_In/Out).
// Cloud Monitor's GetMonitorData accepts the Lighthouse dimension as
// PascalCase "InstanceId" — same as CVM. Note that DescribeBaseMetrics
// reports it as lowercase "instanceid" in the metric metadata, but
// passing the lowercase form to GetMonitorData triggers the misleading
// error "unauthorized operation or the instance has been destroyed".
// Tencent's two APIs disagree about the canonical name; PascalCase is
// what the data API actually serves.
//
// Metric names are CamelCase without underscores (CpuUsage, MemUsage, ...)
// — Tencent's English docs list snake_case names but the API rejects those.
const lighthouseDimensionKey = "InstanceId"

func (c *Client) LighthouseMetricsJSON(ctx context.Context, region, metricName string, minutes int) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	if metricName == "" {
		metricName = "CpuUsage"
	}
	if minutes <= 0 || minutes > 60*24 {
		minutes = 60
	}

	// Gather Lighthouse instances in this region first.
	cl, err := c.Lighthouse()
	if err != nil {
		return "", err
	}
	listReq := lighthouse.NewDescribeInstancesRequest()
	listResp, err := cl.DescribeInstances(listReq)
	if err != nil {
		return "", friendlyError(err)
	}
	type slim struct {
		ID   string
		Name string
	}
	var instances []slim
	if listResp != nil && listResp.Response != nil {
		for _, in := range listResp.Response.InstanceSet {
			id := derefStringRaw(in.InstanceId)
			if id == "" {
				continue
			}
			instances = append(instances, slim{
				ID:   id,
				Name: derefStringRaw(in.InstanceName),
			})
		}
	}
	if len(instances) == 0 {
		out := struct {
			Region string        `json:"region"`
			Metric string        `json:"metric"`
			Items  []interface{} `json:"items"`
		}{c.Region(), metricName, nil}
		b, _ := json.Marshal(out)
		return string(b), nil
	}

	mclient, err := newMonitorClient(c, c.creds.Region)
	if err != nil {
		return "", err
	}

	now := time.Now()
	start := now.Add(-time.Duration(minutes) * time.Minute)
	endStr := now.UTC().Format("2006-01-02T15:04:05Z")
	startStr := start.UTC().Format("2006-01-02T15:04:05Z")
	period := uint64(60)

	ns := "QCE/LIGHTHOUSE"
	req := monitor.NewGetMonitorDataRequest()
	req.Namespace = &ns
	req.MetricName = &metricName
	req.Period = &period
	req.StartTime = &startStr
	req.EndTime = &endStr
	for _, in := range instances {
		instID := in.ID
		dimName := lighthouseDimensionKey
		dim := &monitor.Instance{
			Dimensions: []*monitor.Dimension{
				{Name: &dimName, Value: &instID},
			},
		}
		req.Instances = append(req.Instances, dim)
	}
	resp, err := mclient.GetMonitorData(req)
	if err != nil {
		return "", fmt.Errorf("GetMonitorData: %w", friendlyError(err))
	}

	type item struct {
		InstanceID string  `json:"instance_id"`
		Name       string  `json:"name,omitempty"`
		Latest     float64 `json:"latest,omitempty"`
		Min        float64 `json:"min,omitempty"`
		Max        float64 `json:"max,omitempty"`
		Avg        float64 `json:"avg,omitempty"`
		Samples    int     `json:"samples"`
	}
	byID := map[string]string{}
	for _, in := range instances {
		byID[in.ID] = in.Name
	}
	var items []item
	if resp != nil && resp.Response != nil {
		for _, dp := range resp.Response.DataPoints {
			if dp == nil {
				continue
			}
			instID := ""
			for _, d := range dp.Dimensions {
				if d != nil && d.Name != nil && d.Value != nil &&
					strings.EqualFold(*d.Name, lighthouseDimensionKey) {
					instID = *d.Value
				}
			}
			latest, mn, mx, avg, samples := summarize(dp.Values)
			items = append(items, item{
				InstanceID: instID,
				Name:       byID[instID],
				Latest:     latest,
				Min:        mn,
				Max:        mx,
				Avg:        avg,
				Samples:    samples,
			})
		}
	}
	out := struct {
		Region        string `json:"region"`
		Metric        string `json:"metric"`
		WindowMinutes int    `json:"window_minutes"`
		Items         []item `json:"items"`
	}{c.Region(), metricName, minutes, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
