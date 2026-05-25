package tencent

import (
	"context"
	"encoding/json"
	"fmt"

	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// Public JSON-emitting methods on Client. These are the canonical data
// sources for the HTTP API layer (internal/api/) — they wrap the same SDK
// calls as the CLI list commands but return JSON-encoded summaries instead of
// printing tables.
//
// Each method returns the raw JSON string for a single-typed array of
// resources (or an empty string when no resources exist). Callers that wrap
// the result for HTTP should embed it inside an envelope like
// {"data": <result>} rather than re-encoding.

func (c *Client) JSONCVMs(ctx context.Context) (string, error)             { return c.contextCVMs(ctx) }
func (c *Client) JSONVPCs(ctx context.Context) (string, error)             { return c.contextVPCs(ctx) }
func (c *Client) JSONSecurityGroups(ctx context.Context) (string, error)   { return c.contextSecurityGroups(ctx) }
func (c *Client) JSONMySQL(ctx context.Context) (string, error)            { return c.contextMySQL(ctx) }
func (c *Client) JSONPostgres(ctx context.Context) (string, error)         { return c.contextPostgres(ctx) }
func (c *Client) JSONCOS(ctx context.Context) (string, error)              { return c.contextCOS(ctx) }
func (c *Client) JSONTKE(ctx context.Context) (string, error)              { return c.contextTKE(ctx) }


func (c *Client) JSONCLB(ctx context.Context) (string, error)        { return c.contextCLB(ctx) }
func (c *Client) JSONEIP(ctx context.Context) (string, error)        { return c.contextEIP(ctx) }
func (c *Client) JSONCBS(ctx context.Context) (string, error)        { return c.contextCBS(ctx) }
func (c *Client) JSONSSL(ctx context.Context) (string, error)        { return c.contextSSL(ctx) }
func (c *Client) JSONCAM(ctx context.Context) (string, error)        { return c.contextCAM(ctx) }


func (c *Client) JSONRedis(ctx context.Context) (string, error)      { return c.contextRedis(ctx) }
func (c *Client) JSONMongoDB(ctx context.Context) (string, error)    { return c.contextMongoDB(ctx) }
func (c *Client) JSONCynosDB(ctx context.Context) (string, error)    { return c.contextCynosDB(ctx) }


func (c *Client) JSONCDN(ctx context.Context) (string, error)        { return c.contextCDN(ctx) }
func (c *Client) JSONEdgeOne(ctx context.Context) (string, error)    { return c.contextEdgeOne(ctx) }
func (c *Client) JSONWAF(ctx context.Context) (string, error)        { return c.contextWAF(ctx) }
func (c *Client) JSONAntiDDoS(ctx context.Context) (string, error)   { return c.contextAntiDDoS(ctx) }


func (c *Client) JSONNATGateways(ctx context.Context) (string, error) { return c.contextNAT(ctx) }
func (c *Client) JSONVPNGateways(ctx context.Context) (string, error) { return c.contextVPN(ctx) }
func (c *Client) JSONCCNs(ctx context.Context) (string, error)        { return c.contextCCN(ctx) }
func (c *Client) JSONDirectConnects(ctx context.Context) (string, error) { return c.contextDC(ctx) }


func (c *Client) JSONAlarmPolicies(ctx context.Context) (string, error)  { return c.contextAlarmPolicies(ctx) }
func (c *Client) JSONCLSTopics(ctx context.Context) (string, error)      { return c.contextCLSTopics(ctx) }
func (c *Client) JSONCloudAudit(ctx context.Context) (string, error)     { return c.contextCloudAudit(ctx) }

// Lighthouse (Tencent's lightweight cloud server, separate product from CVM).
// JSONLighthouses is declared in internal/tencent/lighthouse.go directly so it
// can sit next to LighthouseMetricsJSON; we keep this comment as the index entry.

// JSONSGRules returns the ingress + egress policies of a single security
// group plus a `risk` label on each ingress rule that exposes a sensitive
// port to the public internet. Wraps DescribeSecurityGroupPolicies.
func (c *Client) JSONSGRules(ctx context.Context, sgID string) (string, error) {
	client, err := c.VPC()
	if err != nil {
		return "", fmt.Errorf("init vpc client: %w", err)
	}
	req := vpc.NewDescribeSecurityGroupPoliciesRequest()
	req.SecurityGroupId = &sgID
	resp, err := client.DescribeSecurityGroupPolicies(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type rule struct {
		Direction   string `json:"direction"`
		Index       int64  `json:"index"`
		Protocol    string `json:"protocol,omitempty"`
		Port        string `json:"port,omitempty"`
		Source      string `json:"source,omitempty"`
		Action      string `json:"action"`
		Description string `json:"description,omitempty"`
		Risk        string `json:"risk,omitempty"`
	}
	var rows []rule
	risky := 0
	if resp != nil && resp.Response != nil && resp.Response.SecurityGroupPolicySet != nil {
		for _, p := range resp.Response.SecurityGroupPolicySet.Ingress {
			r := buildRule("INGRESS", p, classifySGRule(p, true))
			if r.Risk != "" {
				risky++
			}
			rows = append(rows, r)
		}
		for _, p := range resp.Response.SecurityGroupPolicySet.Egress {
			rows = append(rows, buildRule("EGRESS", p, classifySGRule(p, false)))
		}
	}
	out := map[string]interface{}{
		"sg_id":       sgID,
		"region":      c.Region(),
		"rules":       rows,
		"risky_count": risky,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// FetchKubeconfig retrieves a TKE cluster's kubeconfig. Used by the HTTP API
// layer; the CLI uses getTKEKubeconfig directly so it can print to stdout.
func (c *Client) FetchKubeconfig(ctx context.Context, clusterID string, public bool) (string, error) {
	client, err := newTKEClient(c, c.creds.Region)
	if err != nil {
		return "", err
	}
	req := newDescribeKubeconfigReq(clusterID, public)
	resp, err := client.DescribeClusterKubeconfig(req)
	if err != nil {
		return "", friendlyError(err)
	}
	if resp == nil || resp.Response == nil || resp.Response.Kubeconfig == nil {
		return "", fmt.Errorf("empty kubeconfig response for %s", clusterID)
	}
	return *resp.Response.Kubeconfig, nil
}

func buildRule(dir string, p *vpc.SecurityGroupPolicy, risk string) struct {
	Direction   string `json:"direction"`
	Index       int64  `json:"index"`
	Protocol    string `json:"protocol,omitempty"`
	Port        string `json:"port,omitempty"`
	Source      string `json:"source,omitempty"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	Risk        string `json:"risk,omitempty"`
} {
	idx := int64(0)
	if p != nil && p.PolicyIndex != nil {
		idx = *p.PolicyIndex
	}
	source := derefStringRaw(p.CidrBlock)
	if source == "" {
		source = derefStringRaw(p.Ipv6CidrBlock)
	}
	if source == "" && p.SecurityGroupId != nil {
		source = "sg:" + *p.SecurityGroupId
	}
	return struct {
		Direction   string `json:"direction"`
		Index       int64  `json:"index"`
		Protocol    string `json:"protocol,omitempty"`
		Port        string `json:"port,omitempty"`
		Source      string `json:"source,omitempty"`
		Action      string `json:"action"`
		Description string `json:"description,omitempty"`
		Risk        string `json:"risk,omitempty"`
	}{
		Direction:   dir,
		Index:       idx,
		Protocol:    derefStringRaw(p.Protocol),
		Port:        derefStringRaw(p.Port),
		Source:      source,
		Action:      derefStringRaw(p.Action),
		Description: derefStringRaw(p.PolicyDescription),
		Risk:        risk,
	}
}
