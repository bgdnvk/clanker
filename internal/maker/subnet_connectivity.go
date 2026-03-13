package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// SubnetConnectivity describes whether an instance in a subnet can reach SSM
type SubnetConnectivity struct {
	SubnetID       string
	VpcID          string
	MapPublicIP    bool
	HasNATGateway  bool
	HasSSMEndpoint bool
	CanReachSSM    bool
	RecommendedFix string
}

// CheckSubnetConnectivity verifies if SSM will work for instances in this subnet
func CheckSubnetConnectivity(ctx context.Context, opts ExecOptions, subnetID string) (*SubnetConnectivity, error) {
	result := &SubnetConnectivity{SubnetID: subnetID}

	// Get subnet details
	args := []string{"ec2", "describe-subnets", "--subnet-ids", subnetID,
		"--profile", opts.Profile, "--region", opts.Region, "--output", "json", "--no-cli-pager"}
	out, err := exec.CommandContext(ctx, "aws", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("failed to describe subnet: %w", err)
	}

	var resp struct {
		Subnets []struct {
			VpcId               string `json:"VpcId"`
			MapPublicIpOnLaunch bool   `json:"MapPublicIpOnLaunch"`
		} `json:"Subnets"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || len(resp.Subnets) == 0 {
		return nil, fmt.Errorf("failed to parse subnet response")
	}

	result.VpcID = resp.Subnets[0].VpcId
	result.MapPublicIP = resp.Subnets[0].MapPublicIpOnLaunch

	// If subnet assigns public IPs, SSM should work
	if result.MapPublicIP {
		result.CanReachSSM = true
		return result, nil
	}

	// Check for SSM VPC endpoints
	endpointArgs := []string{"ec2", "describe-vpc-endpoints",
		"--filters", fmt.Sprintf("Name=vpc-id,Values=%s", result.VpcID),
		"--profile", opts.Profile, "--region", opts.Region, "--output", "json", "--no-cli-pager"}
	endpointOut, _ := exec.CommandContext(ctx, "aws", endpointArgs...).Output()

	// Check for ssm, ssmmessages, and ec2messages endpoints
	endpointStr := strings.ToLower(string(endpointOut))
	hasSSM := strings.Contains(endpointStr, "com.amazonaws") && strings.Contains(endpointStr, ".ssm")
	hasSSMMessages := strings.Contains(endpointStr, "ssmmessages")
	hasEC2Messages := strings.Contains(endpointStr, "ec2messages")

	result.HasSSMEndpoint = hasSSM && hasSSMMessages && hasEC2Messages

	if result.HasSSMEndpoint {
		result.CanReachSSM = true
		return result, nil
	}

	// Check for NAT gateway in route table
	result.HasNATGateway = checkNATGateway(ctx, opts, subnetID)

	if result.HasNATGateway {
		result.CanReachSSM = true
		return result, nil
	}

	// Cannot reach SSM
	result.CanReachSSM = false
	result.RecommendedFix = "add --associate-public-ip-address to run-instances"

	return result, nil
}

// checkNATGateway checks if the subnet has a route to a NAT gateway
func checkNATGateway(ctx context.Context, opts ExecOptions, subnetID string) bool {
	// Get route table for subnet
	args := []string{"ec2", "describe-route-tables",
		"--filters", fmt.Sprintf("Name=association.subnet-id,Values=%s", subnetID),
		"--profile", opts.Profile, "--region", opts.Region, "--output", "json", "--no-cli-pager"}
	out, err := exec.CommandContext(ctx, "aws", args...).Output()
	if err != nil {
		return false
	}

	// Check if any route points to a NAT gateway
	return strings.Contains(string(out), "nat-")
}

// RemediateRunInstancesForSSM adds public IP if subnet cannot reach SSM
func RemediateRunInstancesForSSM(ctx context.Context, opts ExecOptions, args []string, w io.Writer) ([]string, error) {
	// Check if --associate-public-ip-address is already present
	for _, arg := range args {
		if arg == "--associate-public-ip-address" {
			return args, nil
		}
	}

	// Find subnet ID in args
	subnetID := ""
	for i, arg := range args {
		if arg == "--subnet-id" && i+1 < len(args) {
			subnetID = args[i+1]
			break
		}
	}

	if subnetID == "" {
		return args, nil // No subnet specified, AWS will pick default
	}

	conn, err := CheckSubnetConnectivity(ctx, opts, subnetID)
	if err != nil {
		// Cannot check, log warning but proceed
		if w != nil {
			fmt.Fprintf(w, "[maker] warning: could not check subnet connectivity: %v\n", err)
		}
		return args, nil
	}

	if conn.CanReachSSM {
		return args, nil
	}

	// Add --associate-public-ip-address
	if w != nil {
		fmt.Fprintf(w, "[maker] subnet %s cannot reach SSM (no public IP, no VPC endpoints, no NAT), adding --associate-public-ip-address\n", subnetID)
	}

	// Insert before --profile to maintain argument order
	for i, arg := range args {
		if arg == "--profile" {
			newArgs := make([]string, 0, len(args)+1)
			newArgs = append(newArgs, args[:i]...)
			newArgs = append(newArgs, "--associate-public-ip-address")
			newArgs = append(newArgs, args[i:]...)
			return newArgs, nil
		}
	}

	return append(args, "--associate-public-ip-address"), nil
}

// ValidateArgsNoConsecutiveFlags checks that flag arguments have values
func ValidateArgsNoConsecutiveFlags(args []string) error {
	flagsRequiringValues := map[string]bool{
		"--user-data":            true,
		"--subnet-id":            true,
		"--security-group-ids":   true,
		"--iam-instance-profile": true,
		"--image-id":             true,
		"--instance-type":        true,
		"--key-name":             true,
		"--block-device-mappings": true,
		"--tag-specifications":   true,
	}

	for i := 0; i < len(args)-1; i++ {
		if flagsRequiringValues[args[i]] {
			next := args[i+1]
			if strings.HasPrefix(next, "--") || next == "" {
				return fmt.Errorf("flag %s has no value (followed by %q)", args[i], next)
			}
		}
	}

	// Check last arg
	if len(args) > 0 && flagsRequiringValues[args[len(args)-1]] {
		return fmt.Errorf("flag %s has no value (at end of args)", args[len(args)-1])
	}

	return nil
}
