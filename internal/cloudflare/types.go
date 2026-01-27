package cloudflare

// ResponseType indicates the type of response from a Cloudflare operation
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
	ResponseTypeError  ResponseType = "error"
)

// Response represents the result of a Cloudflare query or operation
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Plan    interface{}  `json:"plan,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// QueryOptions contains options for Cloudflare queries
type QueryOptions struct {
	ZoneID    string `json:"zone_id,omitempty"`
	ZoneName  string `json:"zone_name,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// CloudflareClient defines the interface for Cloudflare operations
type CloudflareClient interface {
	RunAPI(method, endpoint, body string) (string, error)
	RunWrangler(args ...string) (string, error)
	RunCloudflared(args ...string) (string, error)
	GetAccountID() string
	GetAPIToken() string
}
