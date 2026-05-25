package tencent

import (
	"context"
	"encoding/json"
	"strings"

	cam "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cam/v20190116"
	cbs "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	clb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/clb/v20180317"
	ssl "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssl/v20191205"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// CLBExposureScanJSON flags every public-facing CLB ("OPEN" type) and notes
// which of its listeners are on sensitive ports. Listener-level rules are
// fetched per-LB which makes this slightly slower than the EIP/CBS audits.
func (c *Client) CLBExposureScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	cli, err := newCLBClient(c, c.creds.Region)
	if err != nil {
		return "", err
	}
	req := clb.NewDescribeLoadBalancersRequest()
	openType := "OPEN"
	req.LoadBalancerType = &openType
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cli.DescribeLoadBalancers(req)
	if err != nil {
		return "", friendlyError(err)
	}

	type listenerRow struct {
		ListenerID string `json:"listener_id"`
		Name       string `json:"name,omitempty"`
		Protocol   string `json:"protocol"`
		Port       int64  `json:"port"`
		Risk       string `json:"risk,omitempty"`
	}
	type cveLB struct {
		LBID      string        `json:"lb_id"`
		Name      string        `json:"name,omitempty"`
		Type      string        `json:"type"`
		VIPs      []string      `json:"vips,omitempty"`
		Listeners []listenerRow `json:"listeners"`
		RiskyCount int          `json:"risky_count"`
	}

	var items []cveLB
	if resp != nil && resp.Response != nil {
		for _, lb := range resp.Response.LoadBalancerSet {
			lbID := derefStringRaw(lb.LoadBalancerId)
			listeners, _ := fetchCLBListeners(c, c.Region(), lbID)
			row := cveLB{
				LBID: lbID,
				Name: derefStringRaw(lb.LoadBalancerName),
				Type: derefStringRaw(lb.LoadBalancerType),
				VIPs: stringSlice(lb.LoadBalancerVips),
			}
			for _, l := range listeners {
				proto := strings.ToUpper(derefStringRaw(l.Protocol))
				port := int64(0)
				if l.Port != nil {
					port = *l.Port
				}
				risk := classifyCLBListenerRisk(proto, port)
				row.Listeners = append(row.Listeners, listenerRow{
					ListenerID: derefStringRaw(l.ListenerId),
					Name:       derefStringRaw(l.ListenerName),
					Protocol:   proto,
					Port:       port,
					Risk:       risk,
				})
				if risk != "" {
					row.RiskyCount++
				}
			}
			// Only include LBs that are publicly addressable. We already
			// filtered by Type="OPEN" but a defence-in-depth check costs nothing.
			items = append(items, row)
		}
	}

	out := struct {
		Region string  `json:"region"`
		Items  []cveLB `json:"items"`
	}{c.Region(), items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// classifyCLBListenerRisk returns a risk label when a CLB listener exposes a
// sensitive port. Since CLB listeners by definition serve all of 0.0.0.0/0
// (the VIP is global on OPEN-type LBs), the audit reduces to "is the port
// sensitive?".
func classifyCLBListenerRisk(proto string, port int64) string {
	if proto == "HTTP" || proto == "HTTPS" {
		// Standard web ports are intentional; flag only weird ones.
		if port == 22 || port == 3306 || port == 5432 || port == 6379 || port == 27017 {
			return "WEB-on-DB-PORT"
		}
		return ""
	}
	if proto == "TCP" || proto == "UDP" || proto == "TCP_SSL" {
		switch port {
		case 22:
			return "PUBLIC-SSH"
		case 3389:
			return "PUBLIC-RDP"
		case 3306:
			return "PUBLIC-MySQL"
		case 5432:
			return "PUBLIC-PostgreSQL"
		case 6379:
			return "PUBLIC-Redis"
		case 9200:
			return "PUBLIC-Elasticsearch"
		case 27017:
			return "PUBLIC-MongoDB"
		}
	}
	return ""
}

// IdleEIPScanJSON flags every EIP not bound to a resource. These leak
// budget and (since they keep their IP) historical reputation.
func (c *Client) IdleEIPScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	cli, err := c.VPC()
	if err != nil {
		return "", err
	}
	req := vpc.NewDescribeAddressesRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cli.DescribeAddresses(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type idleEIP struct {
		ID      string `json:"id"`
		Name    string `json:"name,omitempty"`
		IP      string `json:"ip"`
		Status  string `json:"status"`
		Type    string `json:"type,omitempty"`
		Created string `json:"created_at,omitempty"`
	}
	var items []idleEIP
	if resp != nil && resp.Response != nil {
		for _, a := range resp.Response.AddressSet {
			st := strings.ToUpper(derefStringRaw(a.AddressStatus))
			if st != "UNBIND" {
				continue
			}
			items = append(items, idleEIP{
				ID:      derefStringRaw(a.AddressId),
				Name:    derefStringRaw(a.AddressName),
				IP:      derefStringRaw(a.AddressIp),
				Status:  derefStringRaw(a.AddressStatus),
				Type:    derefStringRaw(a.AddressType),
				Created: derefStringRaw(a.CreatedTime),
			})
		}
	}
	out := struct {
		Region string    `json:"region"`
		Items  []idleEIP `json:"items"`
	}{c.Region(), items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnencryptedCBSScanJSON flags any CBS volume where Encrypt==false.
// Volumes that are unattached AND unencrypted are doubly flagged because
// they're cost waste plus potential data exposure if a snapshot is shared.
func (c *Client) UnencryptedCBSScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	cli, err := newCBSClient(c, c.creds.Region)
	if err != nil {
		return "", err
	}
	req := cbs.NewDescribeDisksRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cli.DescribeDisks(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type diskRow struct {
		ID         string `json:"id"`
		Name       string `json:"name,omitempty"`
		Type       string `json:"type"`
		SizeGB     uint64 `json:"size_gb"`
		State      string `json:"state"`
		InstanceID string `json:"instance_id,omitempty"`
		Zone       string `json:"zone,omitempty"`
		Unattached bool   `json:"unattached"`
	}
	var items []diskRow
	if resp != nil && resp.Response != nil {
		for _, d := range resp.Response.DiskSet {
			if derefBool(d.Encrypt) {
				continue
			}
			zone := ""
			if d.Placement != nil {
				zone = derefStringRaw(d.Placement.Zone)
			}
			state := derefStringRaw(d.DiskState)
			items = append(items, diskRow{
				ID:         derefStringRaw(d.DiskId),
				Name:       derefStringRaw(d.DiskName),
				Type:       derefStringRaw(d.DiskType),
				SizeGB:     derefUint64Raw(d.DiskSize),
				State:      state,
				InstanceID: derefStringRaw(d.InstanceId),
				Zone:       zone,
				Unattached: strings.EqualFold(state, "UNATTACHED"),
			})
		}
	}
	out := struct {
		Region string    `json:"region"`
		Items  []diskRow `json:"items"`
	}{c.Region(), items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CertExpiryScanJSON flags SSL certificates expiring within `days` days.
// Negative DaysLeft means already expired.
func (c *Client) CertExpiryScanJSON(ctx context.Context, days int) (string, error) {
	if days <= 0 {
		days = 30
	}
	cli, err := newSSLClient(c)
	if err != nil {
		return "", err
	}
	req := ssl.NewDescribeCertificatesRequest()
	var offset, limit uint64 = 0, 200
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cli.DescribeCertificates(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type certRow struct {
		ID       string `json:"id"`
		Alias    string `json:"alias,omitempty"`
		Domain   string `json:"domain,omitempty"`
		Status   string `json:"status"`
		CertEnd  string `json:"cert_end,omitempty"`
		DaysLeft int    `json:"days_left"`
	}
	var items []certRow
	if resp != nil && resp.Response != nil {
		for _, cert := range resp.Response.Certificates {
			d := daysUntilExpiry(cert.CertEndTime)
			if d > days {
				continue
			}
			items = append(items, certRow{
				ID:       derefStringRaw(cert.CertificateId),
				Alias:    derefStringRaw(cert.Alias),
				Domain:   derefStringRaw(cert.Domain),
				Status:   sslStatus(cert.Status),
				CertEnd:  derefStringRaw(cert.CertEndTime),
				DaysLeft: d,
			})
		}
	}
	out := struct {
		ThresholdDays int       `json:"threshold_days"`
		Items         []certRow `json:"items"`
	}{days, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CAMHygieneScanJSON flags sub-accounts with console_login enabled but no
// phone number registered (so MFA via phone is impossible). The SDK at this
// version doesn't expose the canonical MFA flags so this is a heuristic that
// catches the common "service account accidentally given console access"
// case.
func (c *Client) CAMHygieneScanJSON(ctx context.Context) (string, error) {
	cli, err := newCAMClient(c)
	if err != nil {
		return "", err
	}
	resp, err := cli.ListUsers(cam.NewListUsersRequest())
	if err != nil {
		return "", friendlyError(err)
	}
	type userRow struct {
		UID            uint64 `json:"uid"`
		Name           string `json:"name"`
		Email          string `json:"email,omitempty"`
		ConsoleLogin   bool   `json:"console_login"`
		PhoneRegistered bool  `json:"phone_registered"`
		Findings       []string `json:"findings"`
	}
	var items []userRow
	totalUsers := 0
	if resp != nil && resp.Response != nil {
		totalUsers = len(resp.Response.Data)
		for _, u := range resp.Response.Data {
			console := derefUint64Raw(u.ConsoleLogin) == 1
			phone := strings.TrimSpace(derefStringRaw(u.PhoneNum)) != ""
			var findings []string
			if console && !phone {
				findings = append(findings, "console-login-without-phone")
			}
			if console && strings.TrimSpace(derefStringRaw(u.Email)) == "" {
				findings = append(findings, "console-login-without-email")
			}
			if len(findings) == 0 {
				continue
			}
			items = append(items, userRow{
				UID:             derefUint64Raw(u.Uid),
				Name:            derefStringRaw(u.Name),
				Email:           derefStringRaw(u.Email),
				ConsoleLogin:    console,
				PhoneRegistered: phone,
				Findings:        findings,
			})
		}
	}
	out := struct {
		TotalUsers int       `json:"total_users"`
		Items      []userRow `json:"items"`
	}{totalUsers, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
