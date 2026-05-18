package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ssl "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssl/v20191205"
)

// listSSLCerts prints every SSL certificate the account owns. SSL is a
// global service so no region is needed.
func listSSLCerts(c *Client) error {
	client, err := newSSLClient(c)
	if err != nil {
		return fmt.Errorf("init ssl client: %w", err)
	}
	req := ssl.NewDescribeCertificatesRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeCertificates(req)
	if err != nil {
		return fmt.Errorf("DescribeCertificates: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud SSL Certificates:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.Certificates) == 0 {
		fmt.Println("  No SSL certificates found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CERT_ID\tALIAS\tDOMAIN\tSTATUS\tFROM\tEXPIRES\tDAYS_LEFT")
	for _, cert := range resp.Response.Certificates {
		days := daysUntilExpiry(cert.CertEndTime)
		daysStr := fmt.Sprintf("%d", days)
		if days < 0 {
			daysStr = fmt.Sprintf("EXPIRED %dd ago", -days)
		} else if days < 30 {
			daysStr = fmt.Sprintf("⚠ %dd", days)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			derefString(cert.CertificateId),
			derefString(cert.Alias),
			derefString(cert.Domain),
			sslStatus(cert.Status),
			derefString(cert.From),
			derefString(cert.CertEndTime),
			daysStr,
		)
	}
	return tw.Flush()
}

func newSSLClient(c *Client) (*ssl.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "ssl.tencentcloudapi.com"
	// SSL is global; pass an arbitrary region (the service ignores it).
	return ssl.NewClient(cred, "ap-guangzhou", cpf)
}

func sslStatus(p *uint64) string {
	if p == nil {
		return "-"
	}
	switch *p {
	case 0:
		return "REVIEWING"
	case 1:
		return "ISSUED"
	case 2:
		return "REVIEW_FAILED"
	case 3:
		return "EXPIRED"
	case 10:
		return "REVOKED"
	default:
		return fmt.Sprintf("STATE-%d", *p)
	}
}

// daysUntilExpiry parses Tencent's "YYYY-MM-DD HH:mm:ss" (GMT+8) format and
// returns the integer day delta from now. Negative when expired.
func daysUntilExpiry(end *string) int {
	if end == nil || strings.TrimSpace(*end) == "" {
		return 0
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.UTC
	}
	layouts := []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z", time.RFC3339}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, strings.TrimSpace(*end), loc)
		if err == nil {
			return int(time.Until(t).Hours() / 24)
		}
	}
	return 0
}
