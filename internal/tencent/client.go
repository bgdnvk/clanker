package tencent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

const defaultRegion = "ap-singapore"

// Credentials holds the resolved Tencent Cloud credentials and target region.
//
// SecretKey is redacted in every String() / %v / %+v / json.Marshal output —
// the raw bytes only flow into the SDK's signature path. Add new fields with
// the same care: anything secret-shaped MUST be excluded from the redacted
// shape below.
type Credentials struct {
	SecretID  string
	SecretKey string
	Region    string
}

// String renders Credentials with SecretKey redacted. Reached by any %v /
// %+v / Println formatting — including accidental logs of a Client (which
// embeds Credentials).
func (c Credentials) String() string {
	return fmt.Sprintf("{SecretID:%s SecretKey:**** Region:%s}", c.SecretID, c.Region)
}

// MarshalJSON ensures SecretKey is never serialised verbatim. Returns the
// same shape as String() so dashboards and debug endpoints can safely
// json.Marshal a Credentials value.
func (c Credentials) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SecretID  string `json:"secret_id"`
		SecretKey string `json:"secret_key"`
		Region    string `json:"region"`
	}{
		SecretID:  c.SecretID,
		SecretKey: "****",
		Region:    c.Region,
	})
}

// Client wraps Tencent Cloud SDK clients scoped to a region.
type Client struct {
	creds Credentials
	debug bool
}

// ResolveCredentials reads Tencent credentials from config first, then env.
// Env vars use the names the official Tencent SDK already recognises so the
// same shell session that runs `tccli` will work without extra setup.
func ResolveCredentials() Credentials {
	c := Credentials{
		SecretID:  strings.TrimSpace(viper.GetString("tencent.secret_id")),
		SecretKey: strings.TrimSpace(viper.GetString("tencent.secret_key")),
		Region:    strings.TrimSpace(viper.GetString("tencent.region")),
	}
	if c.SecretID == "" {
		c.SecretID = firstNonEmpty(os.Getenv("TENCENTCLOUD_SECRET_ID"), os.Getenv("TENCENT_SECRET_ID"))
	}
	if c.SecretKey == "" {
		c.SecretKey = firstNonEmpty(os.Getenv("TENCENTCLOUD_SECRET_KEY"), os.Getenv("TENCENT_SECRET_KEY"))
	}
	if c.Region == "" {
		c.Region = firstNonEmpty(os.Getenv("TENCENTCLOUD_REGION"), os.Getenv("TENCENT_REGION"))
	}
	if c.Region == "" {
		c.Region = defaultRegion
	}
	return c
}

// NewClient validates credentials and returns a Tencent client ready to spawn
// per-service SDK clients.
func NewClient(creds Credentials, debug bool) (*Client, error) {
	if creds.SecretID == "" || creds.SecretKey == "" {
		return nil, fmt.Errorf("tencent credentials are required (set tencent.secret_id/tencent.secret_key, or TENCENTCLOUD_SECRET_ID/TENCENTCLOUD_SECRET_KEY)")
	}
	if creds.Region == "" {
		creds.Region = defaultRegion
	}
	return &Client{creds: creds, debug: debug}, nil
}

// BackendTencentCredentials represents Tencent Cloud credentials retrieved
// from the backend credential store (clanker-backend), matching the shape
// every other provider's backend-creds struct has (AWS, GCP, Fly.io, etc.).
type BackendTencentCredentials struct {
	SecretID  string
	SecretKey string
	Region    string
}

// NewClientWithCredentials constructs a Tencent client from backend-provided
// credentials. Mirrors NewClientWithCredentials on the other providers so
// the backend wiring layer can dispatch by provider name without special-
// casing Tencent. Not yet wired into the backend credential flow — kept
// for parity until the backend learns to issue Tencent credentials.
func NewClientWithCredentials(creds *BackendTencentCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}
	if strings.TrimSpace(creds.SecretID) == "" || strings.TrimSpace(creds.SecretKey) == "" {
		return nil, fmt.Errorf("tencent secret_id and secret_key are required")
	}
	region := strings.TrimSpace(creds.Region)
	if region == "" {
		region = defaultRegion
	}
	return &Client{
		creds: Credentials{
			SecretID:  creds.SecretID,
			SecretKey: creds.SecretKey,
			Region:    region,
		},
		debug: debug,
	}, nil
}

// Region returns the region this client is targeting.
func (c *Client) Region() string { return c.creds.Region }

// WithRegion returns a shallow copy of the client scoped to a different region.
// Used by --all-regions fan-out so each region call gets its own SDK client.
func (c *Client) WithRegion(region string) *Client {
	clone := *c
	clone.creds.Region = strings.TrimSpace(region)
	if clone.creds.Region == "" {
		clone.creds.Region = defaultRegion
	}
	return &clone
}

// ListAllRegions queries Tencent for the full set of CVM regions available to
// this credential. The CVM service is used because every region exposes it and
// the API call is cheap.
func (c *Client) ListAllRegions() ([]string, error) {
	cli, err := c.CVM()
	if err != nil {
		return nil, fmt.Errorf("init cvm client for regions: %w", err)
	}
	req := cvm.NewDescribeRegionsRequest()
	resp, err := cli.DescribeRegions(req)
	if err != nil {
		return nil, fmt.Errorf("DescribeRegions: %w", friendlyError(err))
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	var out []string
	for _, r := range resp.Response.RegionSet {
		if r == nil || r.Region == nil {
			continue
		}
		// Skip regions in non-available state (e.g. UNAVAILABLE = closed for new accounts).
		if r.RegionState != nil && strings.EqualFold(*r.RegionState, "UNAVAILABLE") {
			continue
		}
		out = append(out, *r.Region)
	}
	return out, nil
}

// CVM returns a region-scoped CVM SDK client.
func (c *Client) CVM() (*cvm.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("cvm.tencentcloudapi.com")
	return cvm.NewClient(cred, c.creds.Region, cpf)
}

// VPC returns a region-scoped VPC SDK client (also serves subnets + SGs).
func (c *Client) VPC() (*vpc.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("vpc.tencentcloudapi.com")
	return vpc.NewClient(cred, c.creds.Region, cpf)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
