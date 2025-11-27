package semantic

import "maps"

var (
	defaultKeywordWeights = KeywordWeights{
		"error":    1.0,
		"failed":   0.9,
		"warning":  0.7,
		"critical": 1.0,
		"down":     0.9,
		"slow":     0.6,
		"timeout":  0.8,
		"crash":    1.0,
		"debug":    0.3,
		"info":     0.2,
		"success":  0.1,
		"healthy":  0.1,
	}

	defaultContextPatterns = ContextPatterns{
		"troubleshoot": {"error", "failed", "broken", "issue", "problem", "trouble"},
		"monitor":      {"status", "health", "performance", "metrics", "dashboard"},
		"analyze":      {"data", "logs", "patterns", "trends", "analysis"},
		"investigate":  {"investigate", "find", "search", "look", "check"},
	}

	defaultServiceMapping = ServiceMapping{
		"lambda":           {"lambda", "function", "serverless"},
		"ec2":              {"ec2", "instance", "server", "vm"},
		"rds":              {"rds", "database", "db", "mysql", "postgres"},
		"s3":               {"s3", "bucket", "storage", "object"},
		"cloudwatch":       {"cloudwatch", "logs", "metrics", "alarm"},
		"ecs":              {"ecs", "container", "docker", "task"},
		"api_gateway":      {"api", "gateway", "endpoint", "rest"},
		"dynamodb":         {"dynamodb", "ddb", "table", "nosql"},
		"sqs":              {"sqs", "queue", "message"},
		"sns":              {"sns", "topic", "notification"},
		"eks":              {"eks", "kubernetes", "cluster", "k8s"},
		"kinesis":          {"kinesis", "stream", "firehose"},
		"redshift":         {"redshift", "warehouse", "dw", "analytics"},
		"elasticache":      {"elasticache", "redis", "memcached", "cache"},
		"cloudfront":       {"cloudfront", "cdn", "edge"},
		"route53":          {"route 53", "route53", "dns", "resolver"},
		"azure_vm":         {"azure vm", "vm scale set", "virtual machine", "compute"},
		"azure_functions":  {"azure function", "function app", "durable function"},
		"azure_appsvc":     {"app service", "web app", "azure webapp"},
		"azure_sql":        {"azure sql", "sql database", "managed sql"},
		"azure_cosmos":     {"cosmos", "cosmosdb", "cosmos db"},
		"azure_eventhub":   {"event hub", "eventhub", "event hubs"},
		"azure_servicebus": {"service bus", "servicebus", "azure queue"},
		"azure_storage":    {"blob storage", "azure storage", "data lake", "adls"},
		"gcp_compute":      {"gce", "compute engine", "gcp vm"},
		"gcp_cloudrun":     {"cloud run", "run service"},
		"gcp_functions":    {"cloud function", "gcp function"},
		"gcp_pubsub":       {"pubsub", "pub sub", "topic", "subscription"},
		"gcp_bigquery":     {"bigquery", "bq", "dataset"},
		"gcp_spanner":      {"spanner", "cloud spanner"},
		"gcp_gke":          {"gke", "google kubernetes", "kubernetes engine"},
		"gcp_cloudsql":     {"cloud sql", "gcp postgres", "gcp mysql"},
		"oracle_oci":       {"oci", "oracle cloud", "oracle instance"},
		"digitalocean":     {"droplet", "digitalocean", "do kubernetes"},
		"kubernetes":       {"kubernetes", "k8s", "cluster", "namespace"},
	}

	defaultIntentSignals = IntentSignals{
		"troubleshoot": {
			"error":   1.0,
			"failed":  0.9,
			"issue":   0.8,
			"problem": 0.8,
		},
		"monitor": {
			"status":      0.9,
			"health":      0.8,
			"performance": 0.7,
			"metrics":     0.6,
		},
		"analyze": {
			"analyze":  1.0,
			"data":     0.7,
			"patterns": 0.8,
			"trends":   0.6,
		},
	}

	defaultUrgencyKeywords = UrgencyKeywords{
		"critical":  1.0,
		"urgent":    0.9,
		"emergency": 1.0,
		"down":      0.9,
		"outage":    1.0,
		"crash":     0.8,
		"failed":    0.7,
	}

	defaultTimeFrameWords = TimeFrameWords{
		"now":        "real_time",
		"current":    "real_time",
		"latest":     "recent",
		"recent":     "recent",
		"today":      "recent",
		"yesterday":  "recent",
		"last":       "recent",
		"historical": "historical",
		"past":       "historical",
		"old":        "historical",
	}
)

func cloneKeywordWeights(src KeywordWeights) KeywordWeights {
	dst := make(KeywordWeights, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneContextPatterns(src ContextPatterns) ContextPatterns {
	dst := make(ContextPatterns, len(src))
	for k, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		dst[k] = copied
	}
	return dst
}

func cloneServiceMapping(src ServiceMapping) ServiceMapping {
	dst := make(ServiceMapping, len(src))
	for k, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		dst[k] = copied
	}
	return dst
}

func cloneIntentSignals(src IntentSignals) IntentSignals {
	dst := make(IntentSignals, len(src))
	for intent, signals := range src {
		nested := make(map[string]float64, len(signals))
		maps.Copy(nested, signals)
		dst[intent] = nested
	}
	return dst
}

func cloneUrgencyKeywords(src UrgencyKeywords) UrgencyKeywords {
	dst := make(UrgencyKeywords, len(src))
	maps.Copy(dst, src)
	return dst
}

func cloneTimeFrameWords(src TimeFrameWords) TimeFrameWords {
	dst := make(TimeFrameWords, len(src))
	maps.Copy(dst, src)
	return dst
}
