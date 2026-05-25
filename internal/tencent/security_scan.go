package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// ExposedCVM is one CVM with a public IP plus the union of risky inbound
// rules across the security groups attached to it. The frontend renders one
// table row per ExposedCVM.
type ExposedCVM struct {
	InstanceID   string         `json:"instance_id"`
	Name         string         `json:"name"`
	State        string         `json:"state"`
	PublicIP     string         `json:"public_ip"`
	PrivateIP    string         `json:"private_ip,omitempty"`
	SGIDs        []string       `json:"sg_ids"`
	RiskyRules   []ExposedRule  `json:"risky_rules"`
}

// ExposedRule attributes a single risky rule to a (CVM, SG) pair. Port and
// risk strings match the classifier in vpc.go (PUBLIC-SSH, PUBLIC-MySQL, etc).
type ExposedRule struct {
	SGID        string `json:"sg_id"`
	SGName      string `json:"sg_name,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Port        string `json:"port,omitempty"`
	Source      string `json:"source,omitempty"`
	Risk        string `json:"risk"`
	Description string `json:"description,omitempty"`
}

// PublicExposureScanJSON returns a flat list of CVMs that have a public IP
// AND have at least one attached SG with an ingress rule allowing 0.0.0.0/0
// (or ::/0) on a sensitive port. The classifier reuses classifySGRule and
// sensitivePorts from vpc.go so labels are consistent with `tencent sg-rules`.
func (c *Client) PublicExposureScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	cli, err := c.CVM()
	if err != nil {
		return "", err
	}
	req := cvm.NewDescribeInstancesRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cli.DescribeInstances(req)
	if err != nil {
		return "", friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		empty, _ := json.Marshal(struct {
			Region string       `json:"region"`
			Items  []ExposedCVM `json:"items"`
		}{c.Region(), []ExposedCVM{}})
		return string(empty), nil
	}

	// Index SGs by ID so we only fetch each unique SG's rules once.
	sgRules := map[string]*vpc.SecurityGroupPolicySet{}
	sgNames := map[string]string{}
	vpcCli, err := c.VPC()
	if err != nil {
		return "", err
	}

	var exposed []ExposedCVM
	for _, in := range resp.Response.InstanceSet {
		pub := firstIP(in.PublicIpAddresses)
		if pub == "" {
			continue
		}
		sgIDs := stringSlice(in.SecurityGroupIds)
		var risks []ExposedRule
		for _, sgID := range sgIDs {
			rules, ok := sgRules[sgID]
			if !ok {
				rr, name, ferr := fetchSGRules(vpcCli, sgID)
				if ferr != nil {
					// Skip this SG but keep processing others.
					sgRules[sgID] = nil
					continue
				}
				sgRules[sgID] = rr
				sgNames[sgID] = name
				rules = rr
			}
			if rules == nil {
				continue
			}
			for _, p := range rules.Ingress {
				risk := classifySGRule(p, true)
				if risk == "" {
					continue
				}
				risks = append(risks, ExposedRule{
					SGID:        sgID,
					SGName:      sgNames[sgID],
					Protocol:    derefStringRaw(p.Protocol),
					Port:        derefStringRaw(p.Port),
					Source:      sourceCIDR(p),
					Risk:        risk,
					Description: derefStringRaw(p.PolicyDescription),
				})
			}
		}
		if len(risks) == 0 {
			continue
		}
		row := ExposedCVM{
			InstanceID: derefStringRaw(in.InstanceId),
			Name:       derefStringRaw(in.InstanceName),
			State:      derefStringRaw(in.InstanceState),
			PublicIP:   pub,
			PrivateIP:  firstIP(in.PrivateIpAddresses),
			SGIDs:      sgIDs,
			RiskyRules: risks,
		}
		exposed = append(exposed, row)
	}

	out := struct {
		Region string       `json:"region"`
		Items  []ExposedCVM `json:"items"`
	}{
		Region: c.Region(),
		Items:  exposed,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fetchSGRules(vpcCli *vpc.Client, sgID string) (*vpc.SecurityGroupPolicySet, string, error) {
	pReq := vpc.NewDescribeSecurityGroupPoliciesRequest()
	pReq.SecurityGroupId = &sgID
	pResp, err := vpcCli.DescribeSecurityGroupPolicies(pReq)
	if err != nil {
		return nil, "", fmt.Errorf("DescribeSecurityGroupPolicies(%s): %w", sgID, friendlyError(err))
	}
	if pResp == nil || pResp.Response == nil {
		return nil, "", nil
	}
	// Optionally pull the SG name from the DescribeSecurityGroups output —
	// kept cheap by skipping unless the caller wants it.
	return pResp.Response.SecurityGroupPolicySet, "", nil
}

func sourceCIDR(p *vpc.SecurityGroupPolicy) string {
	if p == nil {
		return ""
	}
	if v := derefStringRaw(p.CidrBlock); v != "" {
		return v
	}
	if v := derefStringRaw(p.Ipv6CidrBlock); v != "" {
		return v
	}
	if p.SecurityGroupId != nil {
		return "sg:" + *p.SecurityGroupId
	}
	return ""
}
