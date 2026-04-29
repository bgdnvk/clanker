package maker

import (
	"sort"
	"strconv"
	"strings"
)

// HoursPerMonth matches the cloud-billing convention used in
// internal/k8s/cost and the cost-explorer surface.
const HoursPerMonth = 730.0

// PlanCostItem is one cost-bearing line item derived from a plan
// command. Count is the number of resources spawned by that single
// command (e.g. `--count 3` on run-instances).
type PlanCostItem struct {
	Provider   string  `json:"provider"`
	Resource   string  `json:"resource"`         // "ec2", "rds", "compute-engine", ...
	Family     string  `json:"family,omitempty"` // m5.xlarge, db.t3.medium, e2-small, ...
	Count      int     `json:"count"`
	HourlyUSD  float64 `json:"hourlyUsd,omitempty"`
	MonthlyUSD float64 `json:"monthlyUsd,omitempty"`
	PriceKnown bool    `json:"priceKnown"`
	Note       string  `json:"note,omitempty"` // e.g. "metered" for serverless
}

// PlanCostEstimate is the rollup over all command lines.
type PlanCostEstimate struct {
	HourlyUSD           float64        `json:"hourlyUsd"`
	MonthlyUSD          float64        `json:"monthlyUsd"`
	UnknownPriceItems   int            `json:"unknownPriceItems"`
	UnestimatedCommands int            `json:"unestimatedCommands"`
	Items               []PlanCostItem `json:"items,omitempty"`
	Notes               []string       `json:"notes,omitempty"`
}

// EstimatePlanCost walks a plan and produces a static, on-demand-pricing
// estimate. Intended for "what is this deploy going to cost me before I
// run it" — the prices are coarse and the goal is to surface the
// order-of-magnitude rather than the exact bill.
func EstimatePlanCost(plan *Plan) *PlanCostEstimate {
	out := &PlanCostEstimate{}
	if plan == nil {
		return out
	}
	for _, cmd := range plan.Commands {
		item, ok := classifyCommand(cmd)
		if !ok {
			out.UnestimatedCommands++
			continue
		}
		out.Items = append(out.Items, item)
		out.HourlyUSD += item.HourlyUSD * float64(item.Count)
		out.MonthlyUSD += item.MonthlyUSD * float64(item.Count)
		if !item.PriceKnown {
			out.UnknownPriceItems++
		}
	}

	if out.UnknownPriceItems > 0 {
		out.Notes = append(out.Notes, "some items have no price match — cost is a lower bound; consult your billing console for the full picture")
	}
	if out.UnestimatedCommands > 0 {
		out.Notes = append(out.Notes,
			"plan contains commands the estimator does not recognise (e.g. data-only mutations, IAM role creation, networking). Those carry no compute cost but may incur ancillary charges.")
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return out.Items[i].MonthlyUSD > out.Items[j].MonthlyUSD
	})
	return out
}

// classifyCommand recognises the cost-bearing command shapes we know
// how to price. Returns ok=false for everything else (network,
// IAM-role creation, get/list/describe, etc.) so the caller can count
// them as "unestimated" rather than zero.
func classifyCommand(cmd Command) (PlanCostItem, bool) {
	args := cmd.Args
	if len(args) < 2 {
		return PlanCostItem{}, false
	}
	provider := strings.ToLower(args[0])
	subcommand := strings.ToLower(args[1])

	// AWS commands have already had the leading "aws" trimmed by
	// normalizeArgs in plan.go, so args[0] is the service name (e.g.
	// "ec2", "rds"). For other providers we still see the binary name.
	switch {
	case isAWSService(provider):
		return classifyAWSCommand(args)
	case provider == "gcloud":
		// args[0] = "gcloud", args[1] = service ("compute", "sql", ...)
		return classifyGCPCommand(args)
	case provider == "hcloud":
		return classifyHetznerCommand(args)
	case provider == "doctl":
		return classifyDigitalOceanCommand(args)
	case provider == "vercel":
		return PlanCostItem{
			Provider: "vercel", Resource: "deployment", Count: 1,
			Note: "metered — bandwidth + serverless invocations", PriceKnown: false,
		}, true
	}
	_ = subcommand
	return PlanCostItem{}, false
}

func isAWSService(s string) bool {
	switch s {
	case "ec2", "rds", "elasticache", "lambda", "ecs", "eks", "s3", "s3api", "cloudfront", "dynamodb", "sqs", "sns":
		return true
	}
	return false
}

func classifyAWSCommand(args []string) (PlanCostItem, bool) {
	service := strings.ToLower(args[0])
	subcommand := strings.ToLower(args[1])

	switch service {
	case "ec2":
		if subcommand == "run-instances" {
			instanceType := planCostFlagValue(args, "--instance-type")
			count := planCostIntFlag(args, "--count", 1)
			price, known := awsInstancePrice(instanceType)
			return PlanCostItem{
				Provider:   "aws",
				Resource:   "ec2",
				Family:     instanceType,
				Count:      count,
				HourlyUSD:  price,
				MonthlyUSD: price * HoursPerMonth,
				PriceKnown: known,
			}, true
		}
	case "rds":
		if subcommand == "create-db-instance" {
			class := planCostFlagValue(args, "--db-instance-class")
			price, known := awsRDSPrice(class)
			return PlanCostItem{
				Provider:   "aws",
				Resource:   "rds",
				Family:     class,
				Count:      1,
				HourlyUSD:  price,
				MonthlyUSD: price * HoursPerMonth,
				PriceKnown: known,
			}, true
		}
	case "lambda":
		if subcommand == "create-function" {
			return PlanCostItem{
				Provider: "aws", Resource: "lambda", Count: 1,
				Note:       "metered — pay per invocation + GB-seconds",
				PriceKnown: false,
			}, true
		}
	case "s3", "s3api":
		if subcommand == "mb" || subcommand == "create-bucket" {
			return PlanCostItem{
				Provider: "aws", Resource: "s3", Count: 1,
				Note:       "metered — pay per GB stored + requests",
				PriceKnown: false,
			}, true
		}
	}
	return PlanCostItem{}, false
}

func classifyHetznerCommand(args []string) (PlanCostItem, bool) {
	// `hcloud server create --type cx21 --image ubuntu-24.04`
	if len(args) < 3 || strings.ToLower(args[1]) != "server" || strings.ToLower(args[2]) != "create" {
		return PlanCostItem{}, false
	}
	stype := planCostFlagValue(args, "--type")
	price, known := hetznerServerPrice(stype)
	return PlanCostItem{
		Provider:   "hetzner",
		Resource:   "server",
		Family:     stype,
		Count:      1,
		HourlyUSD:  price,
		MonthlyUSD: price * HoursPerMonth,
		PriceKnown: known,
	}, true
}

func classifyDigitalOceanCommand(args []string) (PlanCostItem, bool) {
	// `doctl compute droplet create <name> --size s-1vcpu-1gb ...`
	if len(args) < 4 || strings.ToLower(args[1]) != "compute" || strings.ToLower(args[2]) != "droplet" || strings.ToLower(args[3]) != "create" {
		return PlanCostItem{}, false
	}
	size := planCostFlagValue(args, "--size")
	price, known := doDropletPrice(size)
	return PlanCostItem{
		Provider:   "digitalocean",
		Resource:   "droplet",
		Family:     size,
		Count:      1,
		HourlyUSD:  price,
		MonthlyUSD: price * HoursPerMonth,
		PriceKnown: known,
	}, true
}

func classifyGCPCommand(args []string) (PlanCostItem, bool) {
	// `gcloud compute instances create <name> --machine-type=e2-small ...`
	if len(args) < 4 || strings.ToLower(args[1]) != "compute" {
		return PlanCostItem{}, false
	}
	if strings.ToLower(args[2]) != "instances" || strings.ToLower(args[3]) != "create" {
		return PlanCostItem{}, false
	}
	mt := planCostFlagValue(args, "--machine-type")
	price, known := gcpInstancePrice(mt)
	return PlanCostItem{
		Provider:   "gcp",
		Resource:   "compute-engine",
		Family:     mt,
		Count:      1,
		HourlyUSD:  price,
		MonthlyUSD: price * HoursPerMonth,
		PriceKnown: known,
	}, true
}

// planCostFlagValue looks up a flag by name, supporting both
// "--foo bar" and "--foo=bar" forms. Returns "" when the flag is
// absent. Local helper so we don't collide with maker/exec.go's
// pre-existing flagValue (which has different semantics).
func planCostFlagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimSpace(strings.TrimPrefix(a, name+"="))
		}
	}
	return ""
}

func planCostIntFlag(args []string, name string, defaultVal int) int {
	v := planCostFlagValue(args, name)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

// Price tables — coarse, us-east-1 / nbg1 region snapshots for the
// most common families. Operators with real billing should override
// at the cmd / API level rather than mutate these tables.

func awsInstancePrice(t string) (float64, bool) {
	table := map[string]float64{
		"t3.micro": 0.0104, "t3.small": 0.0208, "t3.medium": 0.0416, "t3.large": 0.0832, "t3.xlarge": 0.1664, "t3.2xlarge": 0.3328,
		"t4g.micro": 0.0084, "t4g.small": 0.0168, "t4g.medium": 0.0336, "t4g.large": 0.0672,
		"m5.large": 0.096, "m5.xlarge": 0.192, "m5.2xlarge": 0.384, "m5.4xlarge": 0.768,
		"c5.large": 0.085, "c5.xlarge": 0.17, "c5.2xlarge": 0.34,
		"r5.large": 0.126, "r5.xlarge": 0.252,
	}
	p, ok := table[t]
	return p, ok
}

func awsRDSPrice(class string) (float64, bool) {
	table := map[string]float64{
		"db.t3.micro": 0.017, "db.t3.small": 0.034, "db.t3.medium": 0.068, "db.t3.large": 0.136,
		"db.m5.large": 0.171, "db.m5.xlarge": 0.342,
		"db.r5.large": 0.24, "db.r5.xlarge": 0.48,
	}
	p, ok := table[class]
	return p, ok
}

func hetznerServerPrice(t string) (float64, bool) {
	// EUR/hr → USD/hr at rough 1.08 conversion, snapshot prices.
	table := map[string]float64{
		"cx11": 0.0046, "cx21": 0.0084, "cx31": 0.0143, "cx41": 0.0273,
		"cpx11": 0.0061, "cpx21": 0.0103, "cpx31": 0.0181,
	}
	p, ok := table[t]
	return p, ok
}

func doDropletPrice(size string) (float64, bool) {
	table := map[string]float64{
		"s-1vcpu-1gb": 0.00744, // $6/mo
		"s-1vcpu-2gb": 0.01488, // $12
		"s-2vcpu-2gb": 0.02232, // $18
		"s-2vcpu-4gb": 0.02976, // $24
		"s-4vcpu-8gb": 0.07142, // $48 (rounded)
	}
	p, ok := table[size]
	return p, ok
}

func gcpInstancePrice(t string) (float64, bool) {
	table := map[string]float64{
		"e2-micro":      0.00838,
		"e2-small":      0.01675,
		"e2-medium":     0.0335,
		"e2-standard-2": 0.067,
		"e2-standard-4": 0.134,
		"n1-standard-1": 0.0475,
		"n1-standard-2": 0.0950,
	}
	p, ok := table[t]
	return p, ok
}
