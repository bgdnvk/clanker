package clankerbox

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type AgentSpec struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Summary         string   `json:"summary"`
	Runtime         string   `json:"runtime"`
	Status          string   `json:"status"`
	DefaultCommand  []string `json:"defaultCommand,omitempty"`
	RequiredSecrets []string `json:"requiredSecrets,omitempty"`
}

type RegionSpec struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tier   string `json:"tier"`
	LowCO2 bool   `json:"lowCo2,omitempty"`
}

type EndpointSpec struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type SizeSpec struct {
	CPU                  string `json:"cpu"`
	Memory               string `json:"memory"`
	MinInstances         int    `json:"minInstances"`
	MaxInstances         int    `json:"maxInstances"`
	Concurrency          int    `json:"concurrency"`
	RequestTimeoutSecond int    `json:"requestTimeoutSeconds"`
}

type SecuritySpec struct {
	Ingress              string   `json:"ingress"`
	AllowUnauthenticated bool     `json:"allowUnauthenticated"`
	RuntimeUser          string   `json:"runtimeUser"`
	PerBoxServiceAccount bool     `json:"perBoxServiceAccount"`
	NoCloudSQL           bool     `json:"noCloudSql"`
	NoVPCConnector       bool     `json:"noVpcConnector"`
	TerminalMode         string   `json:"terminalMode"`
	Controls             []string `json:"controls"`
}

type ManifestOptions struct {
	ProjectID            string
	Image                string
	ServiceAccountEmail  string
	ArtifactRepository   string
	StateBucket          string
	ControlPlaneBaseURL  string
	RequireAuth          bool
	WebSocketTimeoutMins int
}

type Manifest struct {
	Name                 string            `json:"name"`
	ServiceName          string            `json:"serviceName"`
	Agent                AgentSpec         `json:"agent"`
	Region               RegionSpec        `json:"region"`
	Image                string            `json:"image,omitempty"`
	ProjectID            string            `json:"projectId,omitempty"`
	ServiceAccountEmail  string            `json:"serviceAccountEmail,omitempty"`
	ArtifactRepository   string            `json:"artifactRepository,omitempty"`
	StateBucket          string            `json:"stateBucket,omitempty"`
	RequireAuth          bool              `json:"requireAuth"`
	WebSocketTimeoutMins int               `json:"webSocketTimeoutMins"`
	Size                 SizeSpec          `json:"size"`
	Security             SecuritySpec      `json:"security"`
	Environment          map[string]string `json:"environment"`
	Endpoints            []EndpointSpec    `json:"endpoints"`
	IAMRoles             []string          `json:"iamRoles"`
	Labels               map[string]string `json:"labels"`
}

var serviceNameRE = regexp.MustCompile(`[^a-z0-9-]+`)

func Agents() []AgentSpec {
	return []AgentSpec{
		{
			ID:              "hermes",
			Name:            "Hermes",
			Summary:         "Hermes agent bridge for autonomous infrastructure and code tasks.",
			Runtime:         "python-bridge",
			Status:          "adapter",
			DefaultCommand:  []string{"clanker", "box", "serve", "--agent", "hermes"},
			RequiredSecrets: []string{"OPENAI_API_KEY or ANTHROPIC_API_KEY"},
		},
		{
			ID:              "openclaw",
			Name:            "OpenClaw",
			Summary:         "OpenClaw gateway adapter for browser/control-plane agent sessions.",
			Runtime:         "gateway-adapter",
			Status:          "adapter",
			DefaultCommand:  []string{"clanker", "box", "serve", "--agent", "openclaw"},
			RequiredSecrets: []string{"OPENCLAW_GATEWAY_TOKEN"},
		},
		{
			ID:              "codex",
			Name:            "Codex",
			Summary:         "OpenAI Codex CLI adapter for sandboxed coding tasks.",
			Runtime:         "cli-adapter",
			Status:          "adapter",
			DefaultCommand:  []string{"clanker", "box", "serve", "--agent", "codex"},
			RequiredSecrets: []string{"CODEX_AUTH or OPENAI_API_KEY"},
		},
		{
			ID:              "claude-code",
			Name:            "Claude Code",
			Summary:         "Anthropic Claude Code CLI adapter for code and shell workflows.",
			Runtime:         "cli-adapter",
			Status:          "adapter",
			DefaultCommand:  []string{"clanker", "box", "serve", "--agent", "claude-code"},
			RequiredSecrets: []string{"ANTHROPIC_API_KEY or Claude Code login"},
		},
		{
			ID:             "clanker-cli",
			Name:           "Clanker CLI",
			Summary:        "Native Clanker CLI runtime for cloud operations and MCP-compatible workflows.",
			Runtime:        "native-cli",
			Status:         "ready",
			DefaultCommand: []string{"clanker", "box", "serve", "--agent", "clanker-cli"},
		},
	}
}

func Regions() []RegionSpec {
	return []RegionSpec{
		{ID: "us-central1", Name: "Iowa", Tier: "1", LowCO2: true},
		{ID: "us-east1", Name: "South Carolina", Tier: "1"},
		{ID: "us-east4", Name: "Northern Virginia", Tier: "1"},
		{ID: "us-east5", Name: "Columbus", Tier: "1"},
		{ID: "us-south1", Name: "Dallas", Tier: "1", LowCO2: true},
		{ID: "us-west1", Name: "Oregon", Tier: "1", LowCO2: true},
		{ID: "us-west2", Name: "Los Angeles", Tier: "2"},
		{ID: "us-west3", Name: "Salt Lake City", Tier: "2"},
		{ID: "us-west4", Name: "Las Vegas", Tier: "2"},
		{ID: "northamerica-northeast1", Name: "Montreal", Tier: "2", LowCO2: true},
		{ID: "northamerica-northeast2", Name: "Toronto", Tier: "2", LowCO2: true},
		{ID: "northamerica-south1", Name: "Mexico", Tier: "1"},
		{ID: "southamerica-east1", Name: "Sao Paulo", Tier: "2", LowCO2: true},
		{ID: "southamerica-west1", Name: "Santiago", Tier: "2", LowCO2: true},
		{ID: "europe-west1", Name: "Belgium", Tier: "1", LowCO2: true},
		{ID: "europe-west2", Name: "London", Tier: "2", LowCO2: true},
		{ID: "europe-west3", Name: "Frankfurt", Tier: "2"},
		{ID: "europe-west4", Name: "Netherlands", Tier: "1", LowCO2: true},
		{ID: "europe-west6", Name: "Zurich", Tier: "2", LowCO2: true},
		{ID: "europe-west8", Name: "Milan", Tier: "1"},
		{ID: "europe-west9", Name: "Paris", Tier: "1", LowCO2: true},
		{ID: "europe-west10", Name: "Berlin", Tier: "2"},
		{ID: "europe-west12", Name: "Turin", Tier: "2"},
		{ID: "europe-north1", Name: "Finland", Tier: "1", LowCO2: true},
		{ID: "europe-north2", Name: "Stockholm", Tier: "1", LowCO2: true},
		{ID: "europe-southwest1", Name: "Madrid", Tier: "1", LowCO2: true},
		{ID: "europe-central2", Name: "Warsaw", Tier: "2"},
		{ID: "asia-east1", Name: "Taiwan", Tier: "1"},
		{ID: "asia-east2", Name: "Hong Kong", Tier: "2"},
		{ID: "asia-northeast1", Name: "Tokyo", Tier: "1"},
		{ID: "asia-northeast2", Name: "Osaka", Tier: "1"},
		{ID: "asia-northeast3", Name: "Seoul", Tier: "2"},
		{ID: "asia-south1", Name: "Mumbai", Tier: "1"},
		{ID: "asia-south2", Name: "Delhi", Tier: "2"},
		{ID: "asia-southeast1", Name: "Singapore", Tier: "2"},
		{ID: "asia-southeast2", Name: "Jakarta", Tier: "2"},
		{ID: "asia-southeast3", Name: "Bangkok", Tier: "1"},
		{ID: "australia-southeast1", Name: "Sydney", Tier: "2"},
		{ID: "australia-southeast2", Name: "Melbourne", Tier: "2"},
		{ID: "africa-south1", Name: "Johannesburg", Tier: "2"},
		{ID: "me-west1", Name: "Tel Aviv", Tier: "1"},
		{ID: "me-central1", Name: "Doha", Tier: "2"},
		{ID: "me-central2", Name: "Dammam", Tier: "2"},
	}
}

func AgentByID(raw string) (AgentSpec, bool) {
	id := normalizeID(raw)
	for _, agent := range Agents() {
		if agent.ID == id {
			return agent, true
		}
	}
	return AgentSpec{}, false
}

func RegionByID(raw string) (RegionSpec, bool) {
	id := strings.ToLower(strings.TrimSpace(raw))
	for _, region := range Regions() {
		if region.ID == id {
			return region, true
		}
	}
	return RegionSpec{}, false
}

func NewManifest(name, agentID, regionID string, opts ManifestOptions) (Manifest, error) {
	displayName := strings.TrimSpace(name)
	if displayName == "" {
		return Manifest{}, fmt.Errorf("box name is required")
	}
	agent, ok := AgentByID(agentID)
	if !ok {
		return Manifest{}, fmt.Errorf("unsupported agent %q", strings.TrimSpace(agentID))
	}
	region, ok := RegionByID(regionID)
	if !ok {
		return Manifest{}, fmt.Errorf("unsupported Cloud Run region %q", strings.TrimSpace(regionID))
	}
	timeout := opts.WebSocketTimeoutMins
	if timeout <= 0 {
		timeout = 60
	}
	size := DefaultSize()
	serviceName := ServiceName(displayName, agent.ID)
	env := map[string]string{
		"CLANKER_BOX_NAME":            displayName,
		"CLANKER_BOX_AGENT":           agent.ID,
		"CLANKER_BOX_REGION":          region.ID,
		"CLANKER_BOX_REQUIRE_AUTH":    fmt.Sprintf("%t", opts.RequireAuth),
		"CLANKER_BOX_ENABLE_TERMINAL": "true",
		"CLANKER_BOX_WORKDIR":         "/workspace",
	}
	if opts.ControlPlaneBaseURL != "" {
		env["CLANKER_BOX_CONTROL_PLANE_URL"] = strings.TrimRight(opts.ControlPlaneBaseURL, "/")
	}
	if opts.StateBucket != "" {
		env["CLANKER_BOX_STATE_BUCKET"] = opts.StateBucket
	}

	return Manifest{
		Name:                 displayName,
		ServiceName:          serviceName,
		Agent:                agent,
		Region:               region,
		Image:                strings.TrimSpace(opts.Image),
		ProjectID:            strings.TrimSpace(opts.ProjectID),
		ServiceAccountEmail:  strings.TrimSpace(opts.ServiceAccountEmail),
		ArtifactRepository:   strings.TrimSpace(opts.ArtifactRepository),
		StateBucket:          strings.TrimSpace(opts.StateBucket),
		RequireAuth:          opts.RequireAuth,
		WebSocketTimeoutMins: timeout,
		Size:                 size,
		Security: SecuritySpec{
			Ingress:              "internal-and-cloud-load-balancing",
			AllowUnauthenticated: false,
			RuntimeUser:          "clankerbox",
			PerBoxServiceAccount: true,
			NoCloudSQL:           true,
			NoVPCConnector:       true,
			TerminalMode:         "authenticated-websocket-terminal",
			Controls: []string{
				"One Clanker Box per account during beta.",
				"Cloud Run max instances is capped at 1 with concurrency 1.",
				"Runtime must use a unique per-box service account.",
				"Do not attach Cloud SQL roles, database credentials, or a private VPC connector.",
				"Grant Cloud Run invoker only to the Clanker Cloud control plane.",
			},
		},
		Environment: env,
		Endpoints: []EndpointSpec{
			{Kind: "health", Path: "/healthz", Description: "Cloud Run startup and liveness check."},
			{Kind: "info", Path: "/v1/box/info", Description: "Box metadata, agent adapter, and endpoint contract."},
			{Kind: "message", Path: "/v1/box/messages", Description: "Authenticated request/response agent messages."},
			{Kind: "websocket", Path: "/v1/box/ws", Description: "Authenticated bidirectional agent session stream."},
			{Kind: "terminal", Path: "/v1/box/terminal", Description: "Authenticated WebSocket terminal for SSH-style box access."},
		},
		IAMRoles: []string{
			"roles/logging.logWriter",
			"roles/monitoring.metricWriter",
			"roles/artifactregistry.reader",
			"roles/secretmanager.secretAccessor",
			"roles/storage.objectAdmin",
		},
		Labels: map[string]string{
			"app":    "clanker-box",
			"agent":  agent.ID,
			"region": region.ID,
		},
	}, nil
}

func DefaultSize() SizeSpec {
	return SizeSpec{
		CPU:                  "1",
		Memory:               "2Gi",
		MinInstances:         0,
		MaxInstances:         1,
		Concurrency:          1,
		RequestTimeoutSecond: 3600,
	}
}

func ServiceName(name, agentID string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = serviceNameRE.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "agent"
	}
	agentID = normalizeID(agentID)
	prefix := "clanker-box-" + agentID + "-"
	maxBaseLen := 63 - len(prefix) - 7
	if maxBaseLen < 8 {
		maxBaseLen = 8
	}
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
	}
	sum := sha1.Sum([]byte(name + ":" + agentID))
	return prefix + base + "-" + hex.EncodeToString(sum[:])[:6]
}

func normalizeID(raw string) string {
	id := strings.ToLower(strings.TrimSpace(raw))
	id = strings.ReplaceAll(id, "_", "-")
	switch id {
	case "claude", "claudecode":
		return "claude-code"
	case "clanker", "cli":
		return "clanker-cli"
	case "openai-codex":
		return "codex"
	default:
		return id
	}
}

func SortedAgentIDs() []string {
	ids := make([]string, 0, len(Agents()))
	for _, agent := range Agents() {
		ids = append(ids, agent.ID)
	}
	sort.Strings(ids)
	return ids
}
