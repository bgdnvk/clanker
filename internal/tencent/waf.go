package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	waf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/waf/v20180125"
)

// listWAFHosts prints every WAF-protected host. WAF is account-global.
// DescribeHosts without filter args returns the host list directly.
func listWAFHosts(c *Client) error {
	client, err := newWAFClient(c)
	if err != nil {
		return fmt.Errorf("init waf client: %w", err)
	}
	resp, err := client.DescribeHosts(waf.NewDescribeHostsRequest())
	if err != nil {
		return fmt.Errorf("DescribeHosts: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud WAF Protected Hosts:")
	fmt.Println()
	if resp == nil || resp.Response == nil || resp.Response.HostList == nil || len(resp.Response.HostList) == 0 {
		fmt.Println("  No WAF-protected hosts found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DOMAIN_ID\tDOMAIN\tMAIN_DOMAIN\tMODE\tSTATUS")
	for _, h := range resp.Response.HostList {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			derefString(h.DomainId),
			derefString(h.Domain),
			derefString(h.MainDomain),
			wafMode(h.Mode),
			wafStatus(h.Status),
		)
	}
	return tw.Flush()
}

func newWAFClient(c *Client) (*waf.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("waf.tencentcloudapi.com")
	return waf.NewClient(cred, "ap-guangzhou", cpf)
}

func wafMode(p *uint64) string {
	if p == nil {
		return "-"
	}
	if *p == 0 {
		return "OBSERVE"
	}
	if *p == 1 {
		return "BLOCK"
	}
	return fmt.Sprintf("MODE-%d", *p)
}

func wafStatus(p *uint64) string {
	if p == nil {
		return "-"
	}
	if *p == 0 {
		return "UNBOUND"
	}
	if *p == 1 {
		return "BOUND"
	}
	return fmt.Sprintf("STATE-%d", *p)
}

// listWAFHostNames returns the set of protected hostnames for the audit.
func listWAFHostNames(c *Client) map[string]bool {
	out := map[string]bool{}
	client, err := newWAFClient(c)
	if err != nil {
		return out
	}
	resp, err := client.DescribeHosts(waf.NewDescribeHostsRequest())
	if err != nil || resp == nil || resp.Response == nil || resp.Response.HostList == nil {
		return out
	}
	for _, h := range resp.Response.HostList {
		if d := strings.TrimSpace(derefString(h.Domain)); d != "" && d != "-" {
			out[d] = true
		}
	}
	return out
}
