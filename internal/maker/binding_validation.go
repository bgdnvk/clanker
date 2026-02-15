package maker

import "strings"

func bindingLooksCompatible(key, value string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return false
	}

	if strings.HasSuffix(key, "_ARN") && !strings.HasPrefix(value, "arn:") {
		return false
	}

	switch key {
	case "TG_ARN":
		return strings.Contains(value, ":targetgroup/")
	case "ALB_ARN":
		return strings.Contains(value, ":loadbalancer/")
	}

	if (key == "INSTANCE_ID" || strings.HasSuffix(key, "_INSTANCE_ID")) && !strings.HasPrefix(value, "i-") {
		return false
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
