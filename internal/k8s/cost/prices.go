package cost

import (
	"strings"
)

// NodePriceLookup returns the on-demand hourly USD price of a node, or
// (0, false) if no price is known. Callers can plug in their own lookup
// (e.g. backed by a real billing API or a per-cluster overrides file);
// the built-in DefaultAWSOnDemandPrices is a static estimate covering
// the most common AWS instance types and is documented as approximate.
type NodePriceLookup func(node NodeInfo) (hourlyUSD float64, ok bool)

// DefaultAWSOnDemandPrices is a small static fallback covering common
// AWS instance families. Prices are us-east-1 on-demand list, snapshot
// circa 2024 — operators with a real billing API or up-to-date prices
// should override this via a custom NodePriceLookup. The intent is to
// give operators a sensible default rather than refuse to attribute
// cost when no price source is configured.
//
// Missing entries return (0, false) and the attributor records the pod
// as PriceKnown=false; the dominant-share signal is still useful in
// that case (it surfaces over-requesting workloads regardless of $).
func DefaultAWSOnDemandPrices() NodePriceLookup {
	table := map[string]float64{
		// General purpose
		"t3.micro":   0.0104,
		"t3.small":   0.0208,
		"t3.medium":  0.0416,
		"t3.large":   0.0832,
		"t3.xlarge":  0.1664,
		"t3.2xlarge": 0.3328,
		"m5.large":   0.096,
		"m5.xlarge":  0.192,
		"m5.2xlarge": 0.384,
		"m5.4xlarge": 0.768,
		"m6i.large":  0.096,
		"m6i.xlarge": 0.192,
		// Compute optimised
		"c5.large":   0.085,
		"c5.xlarge":  0.17,
		"c5.2xlarge": 0.34,
		"c6i.large":  0.085,
		"c6i.xlarge": 0.17,
		// Memory optimised
		"r5.large":   0.126,
		"r5.xlarge":  0.252,
		"r5.2xlarge": 0.504,
		"r6i.large":  0.126,
		"r6i.xlarge": 0.252,
	}
	return func(n NodeInfo) (float64, bool) {
		// Only AWS — match on instance type label. Other providers fall
		// through to (0, false) and the operator can plug in their own.
		if !strings.EqualFold(n.Provider, "aws") && n.Provider != "" {
			return 0, false
		}
		if n.InstanceType == "" {
			return 0, false
		}
		p, ok := table[n.InstanceType]
		return p, ok
	}
}

// MapPriceLookup builds a NodePriceLookup from a static instance-type →
// hourlyUSD map. Useful when an operator supplies prices via a config
// file or flag and wants to override the defaults.
func MapPriceLookup(prices map[string]float64) NodePriceLookup {
	return func(n NodeInfo) (float64, bool) {
		if n.InstanceType == "" {
			return 0, false
		}
		p, ok := prices[n.InstanceType]
		return p, ok
	}
}

// CompositePriceLookup tries each lookup in order and returns the first
// hit. Use to chain a user-supplied table over the built-in fallback.
func CompositePriceLookup(lookups ...NodePriceLookup) NodePriceLookup {
	return func(n NodeInfo) (float64, bool) {
		for _, l := range lookups {
			if l == nil {
				continue
			}
			if p, ok := l(n); ok {
				return p, true
			}
		}
		return 0, false
	}
}
