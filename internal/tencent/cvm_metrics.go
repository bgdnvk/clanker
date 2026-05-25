package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	monitor "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/monitor/v20180724"
)

// CVMMetricsJSON pulls one metric (default CPUUsage) for every CVM in the
// region over the last N minutes (default 60). One API call per metric is
// made; instances are bundled in a single request via Instances[]. The
// returned data is the latest sample per instance — enough for a "current
// load" snapshot, not for a full sparkline (a future enhancement could
// add a series endpoint).
func (c *Client) CVMMetricsJSON(ctx context.Context, region, metricName string, minutes int) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	if metricName == "" {
		metricName = "CPUUsage"
	}
	if minutes <= 0 || minutes > 60*24 {
		minutes = 60
	}

	// First gather the CVM IDs in this region.
	cvms, err := c.topoCVMs()
	if err != nil {
		return "", err
	}
	if len(cvms) == 0 {
		out := struct {
			Region string        `json:"region"`
			Metric string        `json:"metric"`
			Items  []interface{} `json:"items"`
		}{c.Region(), metricName, nil}
		b, _ := json.Marshal(out)
		return string(b), nil
	}

	client, err := newMonitorClient(c, c.creds.Region)
	if err != nil {
		return "", err
	}

	now := time.Now()
	start := now.Add(-time.Duration(minutes) * time.Minute)
	endStr := now.UTC().Format("2006-01-02T15:04:05Z")
	startStr := start.UTC().Format("2006-01-02T15:04:05Z")
	period := uint64(60) // 1-minute samples

	// Build the instances slice. Tencent's GetMonitorData accepts
	// [{InstanceId: <id>}] dimension entries for QCE/CVM namespace.
	ns := "QCE/CVM"
	req := monitor.NewGetMonitorDataRequest()
	req.Namespace = &ns
	req.MetricName = &metricName
	req.Period = &period
	req.StartTime = &startStr
	req.EndTime = &endStr
	for _, in := range cvms {
		id := in.ID
		if id == "" {
			continue
		}
		instID := id
		dim := &monitor.Instance{
			Dimensions: []*monitor.Dimension{
				{Name: ptrString("InstanceId"), Value: &instID},
			},
		}
		req.Instances = append(req.Instances, dim)
	}
	resp, err := client.GetMonitorData(req)
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
	byID := map[string]TopologyCVM{}
	for _, in := range cvms {
		byID[in.ID] = in
	}
	var items []item
	if resp != nil && resp.Response != nil {
		for _, dp := range resp.Response.DataPoints {
			if dp == nil {
				continue
			}
			instID := ""
			for _, d := range dp.Dimensions {
				if d != nil && d.Name != nil && *d.Name == "InstanceId" && d.Value != nil {
					instID = *d.Value
				}
			}
			latest, mn, mx, avg, samples := summarize(dp.Values)
			it := item{
				InstanceID: instID,
				Latest:     latest,
				Min:        mn,
				Max:        mx,
				Avg:        avg,
				Samples:    samples,
			}
			if v, ok := byID[instID]; ok {
				it.Name = v.Name
			}
			items = append(items, it)
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

func summarize(vals []*float64) (latest, mn, mx, avg float64, n int) {
	if len(vals) == 0 {
		return 0, 0, 0, 0, 0
	}
	first := true
	for _, v := range vals {
		if v == nil {
			continue
		}
		f := *v
		if first {
			mn, mx = f, f
			first = false
		}
		if f < mn {
			mn = f
		}
		if f > mx {
			mx = f
		}
		avg += f
		latest = f
		n++
	}
	if n > 0 {
		avg /= float64(n)
	}
	return
}

func ptrString(s string) *string { return &s }
