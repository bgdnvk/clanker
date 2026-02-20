package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func ExtractELBv2SubnetsFromArgs(args []string) []string {
	if len(args) < 2 {
		return nil
	}
	for i := 0; i < len(args); i++ {
		if args[i] != "--subnets" {
			continue
		}
		out := make([]string, 0, 4)
		for j := i + 1; j < len(args); j++ {
			if strings.HasPrefix(args[j], "--") {
				break
			}
			v := strings.TrimSpace(args[j])
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}

func FindAttachedInternetGatewayForVPC(ctx context.Context, opts CLIExecOptions, vpcID string, run CLIRunner) (string, error) {
	vpcID = strings.TrimSpace(vpcID)
	if vpcID == "" {
		return "", nil
	}
	if run == nil {
		return "", fmt.Errorf("missing runner")
	}

	q := []string{
		"ec2", "describe-internet-gateways",
		"--filters", fmt.Sprintf("Name=attachment.vpc-id,Values=%s", vpcID),
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := run(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		InternetGateways []struct {
			InternetGatewayId string `json:"InternetGatewayId"`
		} `json:"InternetGateways"`
	}
	if json.Unmarshal([]byte(out), &resp) != nil {
		return "", nil
	}
	if len(resp.InternetGateways) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.InternetGateways[0].InternetGatewayId), nil
}

func EnsureVPCInternetGatewayAndDefaultRoute(ctx context.Context, opts CLIExecOptions, vpcID string, w io.Writer, run CLIRunner) (string, error) {
	vpcID = strings.TrimSpace(vpcID)
	if vpcID == "" {
		return "", fmt.Errorf("missing vpc id")
	}
	if w == nil {
		w = io.Discard
	}
	if run == nil {
		return "", fmt.Errorf("missing runner")
	}

	igwID, _ := FindAttachedInternetGatewayForVPC(ctx, opts, vpcID, run)
	if strings.TrimSpace(igwID) == "" {
		createArgs := []string{"ec2", "create-internet-gateway", "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := run(ctx, createArgs, nil, io.Discard)
		if err != nil {
			return "", err
		}
		var resp struct {
			InternetGateway struct {
				InternetGatewayId string `json:"InternetGatewayId"`
			} `json:"InternetGateway"`
		}
		if jsonErr := json.Unmarshal([]byte(out), &resp); jsonErr == nil {
			igwID = strings.TrimSpace(resp.InternetGateway.InternetGatewayId)
		}
		igwID = strings.TrimSpace(igwID)
		if igwID == "" {
			return "", fmt.Errorf("failed to create internet gateway")
		}

		attachArgs := []string{"ec2", "attach-internet-gateway", "--internet-gateway-id", igwID, "--vpc-id", vpcID, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		if _, err := run(ctx, attachArgs, nil, io.Discard); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(w, "[maker] IGW attached: %s\n", igwID)
	}

	drtArgs := []string{
		"ec2", "describe-route-tables",
		"--filters", fmt.Sprintf("Name=vpc-id,Values=%s", vpcID), "Name=association.main,Values=true",
		"--query", "RouteTables[0].RouteTableId",
		"--output", "text",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	outRT, errRT := run(ctx, drtArgs, nil, io.Discard)
	if errRT != nil {
		return igwID, errRT
	}
	rtID := strings.TrimSpace(outRT)
	if rtID == "" || strings.EqualFold(rtID, "none") {
		return igwID, fmt.Errorf("could not find main route table for vpc %s", vpcID)
	}

	createRoute := []string{"ec2", "create-route", "--route-table-id", rtID, "--destination-cidr-block", "0.0.0.0/0", "--gateway-id", igwID, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	replaceRoute := []string{"ec2", "replace-route", "--route-table-id", rtID, "--destination-cidr-block", "0.0.0.0/0", "--gateway-id", igwID, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	if _, err := run(ctx, createRoute, nil, io.Discard); err != nil {
		if _, err2 := run(ctx, replaceRoute, nil, io.Discard); err2 != nil {
			return igwID, err
		}
	}

	_, _ = fmt.Fprintf(w, "[maker] default route ensured: rtb=%s -> igw=%s\n", rtID, igwID)
	return igwID, nil
}
