package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// sensitivePorts are ports that should never be exposed to 0.0.0.0/0.
// Used by listSGRules to flag risky inbound rules.
var sensitivePorts = map[string]string{
	"22":    "SSH",
	"3306":  "MySQL",
	"3389":  "RDP",
	"5432":  "PostgreSQL",
	"6379":  "Redis",
	"9200":  "Elasticsearch",
	"27017": "MongoDB",
}

func listVPCs(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		v      *vpc.Vpc
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.VPC()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init vpc client: %v", r, err))
			continue
		}
		req := vpc.NewDescribeVpcsRequest()
		offsetStr, limitStr := "0", "100"
		req.Offset = &offsetStr
		req.Limit = &limitStr
		resp, err := client.DescribeVpcs(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, v := range resp.Response.VpcSet {
			rows = append(rows, row{region: r, v: v})
		}
	}

	header := fmt.Sprintf("Tencent Cloud VPCs (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud VPCs (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No VPCs found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tVPC_ID\tNAME\tCIDR\tDEFAULT\tDNS_SERVERS\tCREATED")
	} else {
		fmt.Fprintln(tw, "VPC_ID\tNAME\tCIDR\tDEFAULT\tDNS_SERVERS\tCREATED")
	}
	for _, r := range rows {
		v := r.v
		fields := []string{
			derefString(v.VpcId),
			derefString(v.VpcName),
			derefString(v.CidrBlock),
			fmt.Sprintf("%v", derefBool(v.IsDefault)),
			joinIPs(v.DnsServerSet),
			derefString(v.CreatedTime),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

func listSubnets(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		s      *vpc.Subnet
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.VPC()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init vpc client: %v", r, err))
			continue
		}
		req := vpc.NewDescribeSubnetsRequest()
		resp, err := client.DescribeSubnets(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, s := range resp.Response.SubnetSet {
			rows = append(rows, row{region: r, s: s})
		}
	}

	header := fmt.Sprintf("Tencent Cloud Subnets (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud Subnets (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No subnets found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tSUBNET_ID\tNAME\tVPC_ID\tCIDR\tZONE\tAVAIL_IPS\tDEFAULT")
	} else {
		fmt.Fprintln(tw, "SUBNET_ID\tNAME\tVPC_ID\tCIDR\tZONE\tAVAIL_IPS\tDEFAULT")
	}
	for _, r := range rows {
		s := r.s
		fields := []string{
			derefString(s.SubnetId),
			derefString(s.SubnetName),
			derefString(s.VpcId),
			derefString(s.CidrBlock),
			derefString(s.Zone),
			fmt.Sprintf("%d", derefUint64(s.AvailableIpAddressCount)),
			fmt.Sprintf("%v", derefBool(s.IsDefault)),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

func listSecurityGroups(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		g      *vpc.SecurityGroup
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.VPC()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init vpc client: %v", r, err))
			continue
		}
		req := vpc.NewDescribeSecurityGroupsRequest()
		resp, err := client.DescribeSecurityGroups(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, g := range resp.Response.SecurityGroupSet {
			rows = append(rows, row{region: r, g: g})
		}
	}

	header := fmt.Sprintf("Tencent Cloud Security Groups (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud Security Groups (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No security groups found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tSG_ID\tNAME\tDESCRIPTION\tDEFAULT\tCREATED")
	} else {
		fmt.Fprintln(tw, "SG_ID\tNAME\tDESCRIPTION\tDEFAULT\tCREATED")
	}
	for _, r := range rows {
		g := r.g
		fields := []string{
			derefString(g.SecurityGroupId),
			derefString(g.SecurityGroupName),
			derefString(g.SecurityGroupDesc),
			fmt.Sprintf("%v", derefBool(g.IsDefault)),
			derefString(g.CreatedTime),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

// listSGRules prints ingress + egress rules for a single security group and
// flags rules that expose sensitive ports (22, 3306, 5432, 6379, 27017, etc)
// to the public internet (0.0.0.0/0 or ::/0).
func listSGRules(c *Client, sgID string) error {
	client, err := c.VPC()
	if err != nil {
		return fmt.Errorf("init vpc client: %w", err)
	}
	req := vpc.NewDescribeSecurityGroupPoliciesRequest()
	req.SecurityGroupId = &sgID
	resp, err := client.DescribeSecurityGroupPolicies(req)
	if err != nil {
		return fmt.Errorf("DescribeSecurityGroupPolicies: %w", friendlyError(err))
	}
	if resp == nil || resp.Response == nil || resp.Response.SecurityGroupPolicySet == nil {
		fmt.Printf("Security Group %s (region=%s): no policies returned\n", sgID, c.Region())
		return nil
	}
	policies := resp.Response.SecurityGroupPolicySet

	fmt.Printf("Security Group %s — rule audit (region=%s):\n\n", sgID, c.Region())

	risky := 0
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DIRECTION\tIDX\tPROTOCOL\tPORT\tSOURCE/DEST\tACTION\tDESCRIPTION\tRISK")

	for _, p := range policies.Ingress {
		risk := classifySGRule(p, true)
		if risk != "" {
			risky++
		}
		fmt.Fprintln(tw, sgRuleRow("INGRESS", p, risk))
	}
	for _, p := range policies.Egress {
		risk := classifySGRule(p, false)
		fmt.Fprintln(tw, sgRuleRow("EGRESS", p, risk))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Println()
	if risky > 0 {
		fmt.Printf("⚠️  %d risky ingress rule(s) detected — sensitive port exposed to 0.0.0.0/0\n", risky)
	} else {
		fmt.Println("✓ No public exposure of sensitive ports detected in this security group")
	}
	return nil
}

func sgRuleRow(dir string, p *vpc.SecurityGroupPolicy, risk string) string {
	idx := ""
	if p.PolicyIndex != nil {
		idx = fmt.Sprintf("%d", *p.PolicyIndex)
	}
	source := derefString(p.CidrBlock)
	if source == "-" {
		source = derefString(p.Ipv6CidrBlock)
	}
	if source == "-" && p.SecurityGroupId != nil {
		source = "sg:" + *p.SecurityGroupId
	}
	if risk == "" {
		risk = "-"
	}
	return strings.Join([]string{
		dir,
		idx,
		derefString(p.Protocol),
		derefString(p.Port),
		source,
		derefString(p.Action),
		derefString(p.PolicyDescription),
		risk,
	}, "\t")
}

// classifySGRule returns a non-empty risk label when the rule allows a public
// CIDR (0.0.0.0/0 or ::/0) inbound to a sensitive port.
func classifySGRule(p *vpc.SecurityGroupPolicy, ingress bool) string {
	if !ingress || p == nil {
		return ""
	}
	if p.Action == nil || !strings.EqualFold(*p.Action, "ACCEPT") {
		return ""
	}
	cidr := strings.TrimSpace(derefString(p.CidrBlock))
	cidr6 := strings.TrimSpace(derefString(p.Ipv6CidrBlock))
	publicAll := cidr == "0.0.0.0/0" || cidr6 == "::/0"
	if !publicAll {
		return ""
	}
	port := strings.TrimSpace(derefString(p.Port))
	proto := strings.ToUpper(strings.TrimSpace(derefString(p.Protocol)))

	// "ALL" port or "-1" → everything exposed
	if port == "ALL" || port == "-1" || port == "*" || (proto == "ALL" && port == "-") {
		return "PUBLIC-ALL-PORTS"
	}
	// Check sensitive ports — port can be "22", "22,80", "22-100"
	for sp, name := range sensitivePorts {
		if portMatches(port, sp) {
			return "PUBLIC-" + name
		}
	}
	return ""
}

// portMatches checks whether port spec (e.g. "22", "22,80", "20-30") covers
// the target single port string.
func portMatches(spec, target string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == target {
			return true
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			if len(bounds) == 2 {
				var lo, hi, t int
				if _, err := fmt.Sscanf(bounds[0], "%d", &lo); err != nil {
					continue
				}
				if _, err := fmt.Sscanf(bounds[1], "%d", &hi); err != nil {
					continue
				}
				if _, err := fmt.Sscanf(target, "%d", &t); err != nil {
					continue
				}
				if t >= lo && t <= hi {
					return true
				}
			}
		}
	}
	return false
}

func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

func derefUint64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
