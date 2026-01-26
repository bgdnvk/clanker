package zerotrust

import "time"

// Tunnel represents a Cloudflare Tunnel
type Tunnel struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Status          string       `json:"status"`
	CreatedAt       time.Time    `json:"created_at,omitempty"`
	DeletedAt       *time.Time   `json:"deleted_at,omitempty"`
	Connections     []Connection `json:"connections,omitempty"`
	ConnsActiveAt   *time.Time   `json:"conns_active_at,omitempty"`
	ConnsInactiveAt *time.Time   `json:"conns_inactive_at,omitempty"`
}

// Connection represents a tunnel connection
type Connection struct {
	ID            string    `json:"id"`
	ColoName      string    `json:"colo_name"`
	IsActive      bool      `json:"is_active"`
	ClientID      string    `json:"client_id,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	OpenedAt      time.Time `json:"opened_at,omitempty"`
	OriginIP      string    `json:"origin_ip,omitempty"`
}

// TunnelConfiguration represents tunnel ingress configuration
type TunnelConfiguration struct {
	TunnelID string       `json:"tunnel_id"`
	Config   TunnelConfig `json:"config"`
}

// TunnelConfig contains ingress rules
type TunnelConfig struct {
	Ingress       []IngressRule  `json:"ingress"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
	WARPRouting   *WARPRouting   `json:"warp-routing,omitempty"`
}

// IngressRule defines how traffic is routed
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Path          string         `json:"path,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

// OriginRequest contains origin-specific settings
type OriginRequest struct {
	ConnectTimeout         int    `json:"connectTimeout,omitempty"`
	TLSTimeout             int    `json:"tlsTimeout,omitempty"`
	TCPKeepAlive           int    `json:"tcpKeepAlive,omitempty"`
	NoHappyEyeballs        bool   `json:"noHappyEyeballs,omitempty"`
	KeepAliveTimeout       int    `json:"keepAliveTimeout,omitempty"`
	HTTPHostHeader         string `json:"httpHostHeader,omitempty"`
	OriginServerName       string `json:"originServerName,omitempty"`
	NoTLSVerify            bool   `json:"noTLSVerify,omitempty"`
	DisableChunkedEncoding bool   `json:"disableChunkedEncoding,omitempty"`
	ProxyAddress           string `json:"proxyAddress,omitempty"`
	ProxyPort              int    `json:"proxyPort,omitempty"`
	ProxyType              string `json:"proxyType,omitempty"`
}

// WARPRouting contains WARP routing settings
type WARPRouting struct {
	Enabled bool `json:"enabled"`
}

// AccessApplication represents a Zero Trust Access application
type AccessApplication struct {
	ID                      string    `json:"id"`
	UID                     string    `json:"uid,omitempty"`
	Name                    string    `json:"name"`
	Domain                  string    `json:"domain"`
	Type                    string    `json:"type"` // self_hosted, saas, ssh, vnc, etc.
	SessionDuration         string    `json:"session_duration,omitempty"`
	AllowedIdPs             []string  `json:"allowed_idps,omitempty"`
	AutoRedirectToIdentity  bool      `json:"auto_redirect_to_identity,omitempty"`
	EnableBindingCookie     bool      `json:"enable_binding_cookie,omitempty"`
	HTTPOnlyCookieAttribute bool      `json:"http_only_cookie_attribute,omitempty"`
	LogoURL                 string    `json:"logo_url,omitempty"`
	CreatedAt               time.Time `json:"created_at,omitempty"`
	UpdatedAt               time.Time `json:"updated_at,omitempty"`
}

// AccessPolicy represents a Zero Trust Access policy
type AccessPolicy struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Precedence int          `json:"precedence"`
	Decision   string       `json:"decision"` // allow, deny, non_identity, bypass
	Include    []PolicyRule `json:"include"`
	Exclude    []PolicyRule `json:"exclude,omitempty"`
	Require    []PolicyRule `json:"require,omitempty"`
	CreatedAt  time.Time    `json:"created_at,omitempty"`
	UpdatedAt  time.Time    `json:"updated_at,omitempty"`
}

// PolicyRule defines a rule within a policy
type PolicyRule struct {
	Email        *EmailRule        `json:"email,omitempty"`
	EmailDomain  *EmailDomainRule  `json:"email_domain,omitempty"`
	Everyone     *EveryoneRule     `json:"everyone,omitempty"`
	IPRanges     *IPRangesRule     `json:"ip,omitempty"`
	Country      *CountryRule      `json:"geo,omitempty"`
	Group        *GroupRule        `json:"group,omitempty"`
	ServiceToken *ServiceTokenRule `json:"service_token,omitempty"`
}

// EmailRule matches specific emails
type EmailRule struct {
	Email string `json:"email"`
}

// EmailDomainRule matches email domains
type EmailDomainRule struct {
	Domain string `json:"domain"`
}

// EveryoneRule matches everyone
type EveryoneRule struct{}

// IPRangesRule matches IP ranges
type IPRangesRule struct {
	IP string `json:"ip"`
}

// CountryRule matches countries
type CountryRule struct {
	CountryCode string `json:"country_code"`
}

// GroupRule matches identity provider groups
type GroupRule struct {
	ID string `json:"id"`
}

// ServiceTokenRule matches service tokens
type ServiceTokenRule struct {
	TokenID string `json:"token_id"`
}

// QueryOptions contains options for Zero Trust queries
type QueryOptions struct {
	AccountID string `json:"account_id,omitempty"`
	ZoneID    string `json:"zone_id,omitempty"`
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
	ResponseTypeError  ResponseType = "error"
)

// Response represents the result of a Zero Trust operation
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Plan    *Plan        `json:"plan,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Plan represents a Zero Trust modification plan
type Plan struct {
	Summary  string    `json:"summary"`
	Commands []Command `json:"commands"`
}

// Command represents a single Zero Trust command (cloudflared or API)
type Command struct {
	Tool     string   `json:"tool"` // cloudflared, api
	Args     []string `json:"args,omitempty"`
	Method   string   `json:"method,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
	Body     string   `json:"body,omitempty"`
	Reason   string   `json:"reason"`
}

// QueryAnalysis contains the result of analyzing a Zero Trust query
type QueryAnalysis struct {
	IsReadOnly   bool
	Operation    string // list, get, create, delete, route
	ResourceType string // tunnel, access_app, access_policy
	ResourceName string
}
