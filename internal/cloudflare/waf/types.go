package waf

import "time"

// FirewallRule represents a Cloudflare firewall rule
type FirewallRule struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Action      string    `json:"action"` // block, challenge, js_challenge, managed_challenge, allow, log, bypass
	Priority    int       `json:"priority"`
	Paused      bool      `json:"paused"`
	CreatedOn   time.Time `json:"created_on"`
	ModifiedOn  time.Time `json:"modified_on"`
	Filter      Filter    `json:"filter"`
}

// Filter represents a firewall filter expression
type Filter struct {
	ID          string `json:"id"`
	Expression  string `json:"expression"`
	Description string `json:"description"`
	Paused      bool   `json:"paused"`
}

// RateLimitRule represents a Cloudflare rate limiting rule
type RateLimitRule struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Disabled    bool            `json:"disabled"`
	Threshold   int             `json:"threshold"`
	Period      int             `json:"period"` // in seconds
	Action      RateLimitAction `json:"action"`
	Match       RateLimitMatch  `json:"match"`
}

// RateLimitAction defines what happens when rate limit is triggered
type RateLimitAction struct {
	Mode     string             `json:"mode"` // simulate, ban, challenge, js_challenge, managed_challenge
	Timeout  int                `json:"timeout,omitempty"`
	Response *RateLimitResponse `json:"response,omitempty"`
}

// RateLimitResponse for custom responses
type RateLimitResponse struct {
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

// RateLimitMatch defines what requests to match
type RateLimitMatch struct {
	Request  RateLimitMatchRequest  `json:"request"`
	Response RateLimitMatchResponse `json:"response,omitempty"`
}

// RateLimitMatchRequest defines request matching criteria
type RateLimitMatchRequest struct {
	Methods    []string `json:"methods,omitempty"`
	Schemes    []string `json:"schemes,omitempty"`
	URLPattern string   `json:"url_pattern,omitempty"`
}

// RateLimitMatchResponse defines response matching criteria
type RateLimitMatchResponse struct {
	Statuses      []int `json:"statuses,omitempty"`
	OriginTraffic *bool `json:"origin_traffic,omitempty"`
}

// WAFRule represents a managed WAF rule
type WAFRule struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Priority     int      `json:"priority"`
	Group        WAFGroup `json:"group"`
	PackageID    string   `json:"package_id"`
	Mode         string   `json:"mode"` // on, off, default, disable, simulate, block, challenge
	DefaultMode  string   `json:"default_mode"`
	AllowedModes []string `json:"allowed_modes"`
}

// WAFGroup represents a WAF rule group
type WAFGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WAFPackage represents a WAF package (ruleset)
type WAFPackage struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ZoneID        string `json:"zone_id"`
	DetectionMode string `json:"detection_mode"`
	Sensitivity   string `json:"sensitivity"`
	ActionMode    string `json:"action_mode"`
}

// SecurityLevel represents zone security level settings
type SecurityLevel struct {
	ID         string `json:"id"`
	Value      string `json:"value"` // off, essentially_off, low, medium, high, under_attack
	Editable   bool   `json:"editable"`
	ModifiedOn string `json:"modified_on"`
}

// QueryOptions contains options for WAF queries
type QueryOptions struct {
	ZoneID   string `json:"zone_id,omitempty"`
	ZoneName string `json:"zone_name,omitempty"`
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
	ResponseTypeError  ResponseType = "error"
)

// Response represents the result of a WAF operation
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Plan    *Plan        `json:"plan,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Plan represents a WAF modification plan
type Plan struct {
	Summary  string    `json:"summary"`
	Commands []Command `json:"commands"`
}

// Command represents a single WAF API command
type Command struct {
	Method   string `json:"method"`
	Endpoint string `json:"endpoint"`
	Body     string `json:"body,omitempty"`
	Reason   string `json:"reason"`
}

// QueryAnalysis contains the result of analyzing a WAF query
type QueryAnalysis struct {
	IsReadOnly   bool
	Operation    string // list, get, create, update, delete, enable, disable
	ResourceType string // firewall-rule, rate-limit, waf-rule, security-level
	ZoneName     string
	RuleID       string
	Action       string // block, challenge, allow, etc.
	Expression   string
	Description  string
}
