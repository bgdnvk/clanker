package maker

import (
	"regexp"
	"strings"
)

var uuidBindingValueRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var ec2InstanceIDBindingValueRe = regexp.MustCompile(`(?i)^i-[0-9a-f]{8,17}$`)

func isPlaceholderSentinelValue(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	normalized = strings.Trim(normalized, "<>\"'` ")
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, "/", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	switch normalized {
	case "", "UNKNOWN", "UNRESOLVED", "NOT_FOUND", "NOTFOUND", "NONE", "NULL", "NIL", "NA", "N_A", "TBD", "TODO", "PLACEHOLDER", "REPLACE_ME":
		return true
	default:
		return false
	}
}

func bindingLooksCompatible(key, value string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return false
	}
	if isPlaceholderSentinelValue(value) {
		return false
	}

	if strings.HasSuffix(key, "_ARN") && !strings.HasPrefix(value, "arn:") {
		return false
	}
	if strings.Contains(key, "UUID") && !uuidBindingValueRe.MatchString(value) {
		return false
	}

	switch key {
	case "TG_ARN":
		return strings.Contains(value, ":targetgroup/")
	case "ALB_ARN":
		return strings.Contains(value, ":loadbalancer/")
	}

	if key == "INSTANCE_ID" || strings.HasSuffix(key, "_INSTANCE_ID") {
		if !ec2InstanceIDBindingValueRe.MatchString(value) {
			return false
		}
		if isAllZeroHex(strings.TrimPrefix(strings.ToLower(value), "i-")) {
			return false
		}
	}
	if (key == "SG_ID" || (strings.Contains(key, "SG") && strings.HasSuffix(key, "_ID"))) && !strings.HasPrefix(value, "sg-") {
		return false
	}
	if strings.Contains(key, "SUBNET") && strings.HasSuffix(key, "_ID") && !strings.HasPrefix(value, "subnet-") {
		return false
	}
	if strings.Contains(key, "VPC") && strings.HasSuffix(key, "_ID") && !strings.HasPrefix(value, "vpc-") {
		return false
	}

	return true
}

func isAllZeroHex(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch != '0' {
			return false
		}
	}
	return true
}
