package tencent

import (
	"context"
	"encoding/json"
	"strings"

	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// WAFCoverageScanJSON flags every CDN/EdgeOne domain that doesn't appear
// in the WAF-protected hosts list.
func (c *Client) WAFCoverageScanJSON(ctx context.Context) (string, error) {
	cdnDomains := listCDNDomainNames(c)
	teoZones := listEdgeOneZoneNames(c)
	wafProtected := listWAFHostNames(c)

	type uncovered struct {
		Domain string `json:"domain"`
		Source string `json:"source"`
	}
	var items []uncovered
	for _, d := range cdnDomains {
		if !isDomainCoveredByWAF(d, wafProtected) {
			items = append(items, uncovered{Domain: d, Source: "cdn"})
		}
	}
	for _, d := range teoZones {
		if !isDomainCoveredByWAF(d, wafProtected) {
			items = append(items, uncovered{Domain: d, Source: "edgeone"})
		}
	}
	wafList := make([]string, 0, len(wafProtected))
	for k := range wafProtected {
		wafList = append(wafList, k)
	}
	out := struct {
		WAFProtected []string    `json:"waf_protected"`
		Items        []uncovered `json:"items"`
	}{wafList, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func isDomainCoveredByWAF(domain string, waf map[string]bool) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return true
	}
	for w := range waf {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if w == domain || strings.HasSuffix(domain, "."+w) {
			return true
		}
	}
	return false
}

// AntiDDoSCoverageScanJSON returns the high-signal posture answer: "is
// there any Anti-DDoS Advanced coverage at all?" If not, every public IP
// is on Basic protection (~2 Gbps free). When Advanced subscriptions
// exist, the audit lists public CVMs + EIPs in the region as "may not be
// protected" (definitive per-IP attribution would need additional API
// calls — a v2 enhancement).
func (c *Client) AntiDDoSCoverageScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	hasAdvanced, advancedIDs, _ := hasAntiDDoSAdvanced(c)

	type publicTarget struct {
		Kind     string `json:"kind"` // "cvm" or "eip"
		ID       string `json:"id"`
		Name     string `json:"name,omitempty"`
		PublicIP string `json:"public_ip"`
	}
	var targets []publicTarget

	if cli, err := c.CVM(); err == nil {
		req := cvm.NewDescribeInstancesRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		if resp, e := cli.DescribeInstances(req); e == nil && resp != nil && resp.Response != nil {
			for _, in := range resp.Response.InstanceSet {
				pip := firstIP(in.PublicIpAddresses)
				if pip == "" {
					continue
				}
				targets = append(targets, publicTarget{
					Kind:     "cvm",
					ID:       derefStringRaw(in.InstanceId),
					Name:     derefStringRaw(in.InstanceName),
					PublicIP: pip,
				})
			}
		}
	}

	if cli, err := c.VPC(); err == nil {
		req := vpc.NewDescribeAddressesRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		if resp, e := cli.DescribeAddresses(req); e == nil && resp != nil && resp.Response != nil {
			for _, a := range resp.Response.AddressSet {
				if strings.ToUpper(derefStringRaw(a.AddressStatus)) != "BIND" {
					continue
				}
				ip := derefStringRaw(a.AddressIp)
				if ip == "" {
					continue
				}
				targets = append(targets, publicTarget{
					Kind:     "eip",
					ID:       derefStringRaw(a.AddressId),
					Name:     derefStringRaw(a.AddressName),
					PublicIP: ip,
				})
			}
		}
	}

	posture := "BASIC_ONLY"
	if hasAdvanced {
		posture = "MIXED"
	}
	if hasAdvanced && len(targets) == 0 {
		posture = "ADVANCED_SUBSCRIBED_NO_PUBLIC"
	}
	out := struct {
		Region            string         `json:"region"`
		Posture           string         `json:"posture"`
		HasAdvanced       bool           `json:"has_advanced"`
		AdvancedInstances []string       `json:"advanced_instances,omitempty"`
		PublicTargets     []publicTarget `json:"public_targets"`
	}{
		Region:            c.Region(),
		Posture:           posture,
		HasAdvanced:       hasAdvanced,
		AdvancedInstances: advancedIDs,
		PublicTargets:     targets,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
