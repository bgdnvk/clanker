package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"

	"gopkg.in/yaml.v3"
)

type vpcDescribeResp struct {
	Vpcs []struct {
		VpcId                   string `json:"VpcId"`
		CidrBlock               string `json:"CidrBlock"`
		CidrBlockAssociationSet []struct {
			CidrBlock string `json:"CidrBlock"`
		} `json:"CidrBlockAssociationSet"`
		Ipv6CidrBlockAssociationSet []struct {
			Ipv6CidrBlock string `json:"Ipv6CidrBlock"`
		} `json:"Ipv6CidrBlockAssociationSet"`
	} `json:"Vpcs"`
}

type subnetsDescribeResp struct {
	Subnets []struct {
		SubnetId  string `json:"SubnetId"`
		VpcId     string `json:"VpcId"`
		CidrBlock string `json:"CidrBlock"`
	} `json:"Subnets"`
}

func remediateEC2AssociateVpcCidrBlockInvalidRangeAndRetry(
	ctx context.Context,
	opts ExecOptions,
	args []string,
	stdinBytes []byte,
	w io.Writer,
) error {
	vpcID := strings.TrimSpace(flagValue(args, "--vpc-id"))
	desiredCIDR := strings.TrimSpace(flagValue(args, "--cidr-block"))
	if vpcID == "" || desiredCIDR == "" {
		return fmt.Errorf("cannot remediate associate-vpc-cidr-block: missing --vpc-id/--cidr-block")
	}

	cidrs, primary, err := describeVPCCIDRs(ctx, opts, vpcID)
	if err != nil {
		return err
	}
	prefixLen, err := cidrPrefixLen(desiredCIDR)
	if err != nil {
		return err
	}

	picked, ok := pickAdditionalCIDRInSamePrivateRange(primary, cidrs, prefixLen)
	if !ok {
		return fmt.Errorf("cannot pick replacement cidr (primary=%s desired=%s)", primary, desiredCIDR)
	}

	rewritten := setFlagValue(args, "--cidr-block", picked)
	rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	_, _ = fmt.Fprintf(w, "[maker] remediation attempted: rewrite associate-vpc-cidr-block cidr %s -> %s then retry\n", desiredCIDR, picked)
	_, err = runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer)
	return err
}

func remediateCloudFormationTemplateSubnetCIDRsAndRetry(
	ctx context.Context,
	opts ExecOptions,
	args []string,
	stdinBytes []byte,
	w io.Writer,
) error {
	body := strings.TrimSpace(flagValue(args, "--template-body"))
	if body == "" {
		return fmt.Errorf("cannot remediate create-stack: missing --template-body")
	}

	tmpl, err := parseCloudFormationTemplateBody(body)
	if err != nil {
		return err
	}

	vpcID := findCloudFormationVpcID(tmpl)
	if strings.TrimSpace(vpcID) == "" {
		return fmt.Errorf("cannot remediate create-stack: unable to infer vpc id from template")
	}

	vpcCIDRs, _, err := describeVPCCIDRs(ctx, opts, vpcID)
	if err != nil {
		return err
	}
	existingSubnetCIDRs, _ := describeSubnetCIDRs(ctx, opts, vpcID)

	c1, c2, ok := pickTwoSubnetsCIDRs(vpcCIDRs, existingSubnetCIDRs, 24)
	if !ok {
		return fmt.Errorf("cannot pick 2 free /24 cidrs for template (vpc=%s)", vpcID)
	}

	if ok := rewriteCloudFormationSubnetCidrs(tmpl, c1, c2); !ok {
		return fmt.Errorf("cannot remediate create-stack: no subnet resources found to rewrite")
	}

	b, err := json.Marshal(tmpl)
	if err != nil {
		return err
	}

	rewrittenArgs := setFlagValue(args, "--template-body", string(b))
	// If this was a create-stack and the stack already exists (common after ROLLBACK_COMPLETE),
	// switch to update-stack.
	if len(rewrittenArgs) >= 2 && rewrittenArgs[0] == "cloudformation" && rewrittenArgs[1] == "create-stack" {
		stackName := strings.TrimSpace(flagValue(rewrittenArgs, "--stack-name"))
		if stackName != "" && cloudformationStackExists(ctx, opts, stackName) {
			rewrittenArgs = append([]string{}, rewrittenArgs...)
			rewrittenArgs[1] = "update-stack"
		}
	}
	rewrittenAWSArgs := append(append([]string{}, rewrittenArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	_, _ = fmt.Fprintf(w, "[maker] remediation attempted: rewrite cloudformation subnet cidrs to %s and %s then retry\n", c1, c2)
	_, err = runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer)
	return err
}

func parseCloudFormationTemplateBody(body string) (map[string]any, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("empty template body")
	}

	// Try JSON first.
	var j map[string]any
	if err := json.Unmarshal([]byte(body), &j); err == nil && len(j) > 0 {
		return j, nil
	}

	// Then try YAML.
	var y any
	if err := yaml.Unmarshal([]byte(body), &y); err != nil {
		return nil, fmt.Errorf("cannot parse template-body as json or yaml: %w", err)
	}
	clean := yamlToJSONCompatible(y)
	m, ok := clean.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("template-body must be an object")
	}
	return m, nil
}

func yamlToJSONCompatible(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range vv {
			out[k] = yamlToJSONCompatible(x)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, x := range vv {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			out[ks] = yamlToJSONCompatible(x)
		}
		return out
	case []any:
		out := make([]any, 0, len(vv))
		for _, x := range vv {
			out = append(out, yamlToJSONCompatible(x))
		}
		return out
	default:
		return vv
	}
}

func describeVPCCIDRs(ctx context.Context, opts ExecOptions, vpcID string) (all []string, primary string, err error) {
	q := []string{"ec2", "describe-vpcs", "--vpc-ids", vpcID, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return nil, "", err
	}

	var resp vpcDescribeResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, "", err
	}
	if len(resp.Vpcs) == 0 {
		return nil, "", fmt.Errorf("vpc not found: %s", vpcID)
	}
	v := resp.Vpcs[0]
	primary = strings.TrimSpace(v.CidrBlock)
	if primary != "" {
		all = append(all, primary)
	}
	for _, a := range v.CidrBlockAssociationSet {
		c := strings.TrimSpace(a.CidrBlock)
		if c != "" {
			all = append(all, c)
		}
	}
	all = dedupeStrings(all)
	return all, primary, nil
}

func describeSubnetCIDRs(ctx context.Context, opts ExecOptions, vpcID string) ([]string, error) {
	q := []string{"ec2", "describe-subnets", "--filters", "Name=vpc-id,Values=" + vpcID, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return nil, err
	}
	var resp subnetsDescribeResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	var cidrs []string
	for _, s := range resp.Subnets {
		c := strings.TrimSpace(s.CidrBlock)
		if c != "" {
			cidrs = append(cidrs, c)
		}
	}
	return dedupeStrings(cidrs), nil
}

func cidrPrefixLen(cidr string) (int, error) {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return 0, err
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return 0, fmt.Errorf("only ipv4 supported")
	}
	return ones, nil
}

func pickAdditionalCIDRInSamePrivateRange(primary string, existing []string, desiredPrefix int) (string, bool) {
	base, limit, ok := privateRangeBounds(primary)
	if !ok {
		return "", false
	}
	if desiredPrefix < 16 {
		desiredPrefix = 16
	}
	if desiredPrefix > 28 {
		desiredPrefix = 28
	}

	step := uint32(1) << uint32(32-desiredPrefix)
	for ip := base; ip <= limit; {
		cand := fmt.Sprintf("%s/%d", uint32ToIPv4(ip).String(), desiredPrefix)
		if !cidrOverlapsAny(cand, existing) {
			return cand, true
		}
		if limit-ip < step {
			break
		}
		ip += step
	}
	return "", false
}

func privateRangeBounds(primary string) (base uint32, limit uint32, ok bool) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(primary))
	if err != nil {
		return 0, 0, false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, 0, false
	}

	// Determine which RFC1918 block the primary belongs to.
	p := ipv4ToUint32(ip4)
	if cidrContains("10.0.0.0/8", ip4) {
		return ipv4ToUint32(net.IPv4(10, 0, 0, 0)), ipv4ToUint32(net.IPv4(10, 255, 255, 255)), true
	}
	if cidrContains("172.16.0.0/12", ip4) {
		return ipv4ToUint32(net.IPv4(172, 16, 0, 0)), ipv4ToUint32(net.IPv4(172, 31, 255, 255)), true
	}
	if cidrContains("192.168.0.0/16", ip4) {
		return ipv4ToUint32(net.IPv4(192, 168, 0, 0)), ipv4ToUint32(net.IPv4(192, 168, 255, 255)), true
	}

	_ = p
	_ = ipnet
	return 0, 0, false
}

func cidrOverlapsAny(cidr string, existing []string) bool {
	for _, e := range existing {
		if cidrOverlaps(cidr, e) {
			return true
		}
	}
	return false
}

func cidrOverlaps(a, b string) bool {
	a0, a1, okA := cidrRange(a)
	b0, b1, okB := cidrRange(b)
	if !okA || !okB {
		return false
	}
	return !(a1 < b0 || b1 < a0)
}

func cidrRange(cidr string) (start uint32, end uint32, ok bool) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return 0, 0, false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, 0, false
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return 0, 0, false
	}
	netIP := ip4.Mask(ipnet.Mask)
	start = ipv4ToUint32(netIP)
	size := uint32(1) << uint32(32-ones)
	end = start + size - 1
	return start, end, true
}

func ipv4ToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}

func uint32ToIPv4(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func cidrContains(cidr string, ip net.IP) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return n.Contains(ip)
}

func findCloudFormationVpcID(tmpl map[string]any) string {
	resources, _ := tmpl["Resources"].(map[string]any)
	for _, rv := range resources {
		res, ok := rv.(map[string]any)
		if !ok {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		if props == nil {
			continue
		}
		if v, ok := props["VpcId"].(string); ok {
			v = strings.TrimSpace(v)
			if strings.HasPrefix(v, "vpc-") {
				return v
			}
		}
	}
	return ""
}

func rewriteCloudFormationSubnetCidrs(tmpl map[string]any, cidr1 string, cidr2 string) bool {
	resources, ok := tmpl["Resources"].(map[string]any)
	if !ok {
		return false
	}

	picked := 0
	for _, rv := range resources {
		res, ok := rv.(map[string]any)
		if !ok {
			continue
		}
		if typ, ok := res["Type"].(string); !ok || typ != "AWS::EC2::Subnet" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		if props == nil {
			continue
		}
		switch picked {
		case 0:
			props["CidrBlock"] = cidr1
			picked++
		case 1:
			props["CidrBlock"] = cidr2
			picked++
		default:
			// leave others
		}
		if picked >= 2 {
			return true
		}
	}
	return false
}

func pickTwoSubnetsCIDRs(vpcCIDRs []string, existingSubnetCIDRs []string, subnetPrefix int) (string, string, bool) {
	if subnetPrefix < 16 {
		subnetPrefix = 16
	}
	if subnetPrefix > 28 {
		subnetPrefix = 28
	}

	var picked []string
	for _, vpcCIDR := range vpcCIDRs {
		vpcStart, vpcEnd, ok := cidrRange(vpcCIDR)
		if !ok {
			continue
		}
		step := uint32(1) << uint32(32-subnetPrefix)
		for ip := vpcStart; ip <= vpcEnd; {
			cand := fmt.Sprintf("%s/%d", uint32ToIPv4(ip).String(), subnetPrefix)
			if !cidrOverlapsAny(cand, existingSubnetCIDRs) {
				picked = append(picked, cand)
				if len(picked) >= 2 {
					return picked[0], picked[1], true
				}
			}
			if vpcEnd-ip < step {
				break
			}
			ip += step
		}
	}
	return "", "", false
}

func setFlagValue(args []string, flag string, value string) []string {
	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		if out[i] == flag {
			if i+1 < len(out) {
				out[i+1] = value
				return out
			}
			out = append(out, value)
			return out
		}
		if strings.HasPrefix(out[i], flag+"=") {
			out[i] = flag + "=" + value
			return out
		}
	}
	out = append(out, flag, value)
	return out
}
