package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	cdn "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdn/v20180606"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// listCDNDomains prints every CDN domain. CDN is account-global.
func listCDNDomains(c *Client) error {
	client, err := newCDNClient(c)
	if err != nil {
		return fmt.Errorf("init cdn client: %w", err)
	}
	req := cdn.NewDescribeDomainsRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeDomains(req)
	if err != nil {
		return fmt.Errorf("DescribeDomains: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud CDN Domains:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.Domains) == 0 {
		fmt.Println("  No CDN domains found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DOMAIN_ID\tDOMAIN\tCNAME\tSTATUS\tSERVICE\tCREATED")
	for _, d := range resp.Response.Domains {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			derefString(d.ResourceId),
			derefString(d.Domain),
			derefString(d.Cname),
			derefString(d.Status),
			derefString(d.ServiceType),
			derefString(d.CreateTime),
		)
	}
	return tw.Flush()
}

func newCDNClient(c *Client) (*cdn.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("cdn.tencentcloudapi.com")
	return cdn.NewClient(cred, "ap-guangzhou", cpf) // CDN ignores region
}

// listCDNDomainNames returns just the domain names (used by audits).
func listCDNDomainNames(c *Client) []string {
	client, err := newCDNClient(c)
	if err != nil {
		return nil
	}
	req := cdn.NewDescribeDomainsRequest()
	var offset, limit int64 = 0, 200
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeDomains(req)
	if err != nil || resp == nil || resp.Response == nil {
		return nil
	}
	out := make([]string, 0, len(resp.Response.Domains))
	for _, d := range resp.Response.Domains {
		if s := strings.TrimSpace(derefString(d.Domain)); s != "" && s != "-" {
			out = append(out, s)
		}
	}
	return out
}
