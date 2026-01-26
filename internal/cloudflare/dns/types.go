package dns

import "time"

// Zone represents a Cloudflare DNS zone
type Zone struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Status            string    `json:"status"`
	Paused            bool      `json:"paused"`
	Type              string    `json:"type"`
	DevelopmentMode   int       `json:"development_mode"`
	NameServers       []string  `json:"name_servers"`
	OriginalNameServers []string `json:"original_name_servers"`
	CreatedOn         time.Time `json:"created_on"`
	ModifiedOn        time.Time `json:"modified_on"`
	Plan              ZonePlan  `json:"plan"`
}

// ZonePlan represents the Cloudflare plan for a zone
type ZonePlan struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Price     int    `json:"price"`
	Currency  string `json:"currency"`
	Frequency string `json:"frequency"`
}

// DNSRecord represents a Cloudflare DNS record
type DNSRecord struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Content    string    `json:"content"`
	Proxied    bool      `json:"proxied"`
	Proxiable  bool      `json:"proxiable"`
	TTL        int       `json:"ttl"`
	Locked     bool      `json:"locked"`
	ZoneID     string    `json:"zone_id"`
	ZoneName   string    `json:"zone_name"`
	CreatedOn  time.Time `json:"created_on"`
	ModifiedOn time.Time `json:"modified_on"`
	Priority   *int      `json:"priority,omitempty"` // For MX records
	Data       *RecordData `json:"data,omitempty"`   // For SRV, CAA, etc.
}

// RecordData contains additional data for certain record types
type RecordData struct {
	// SRV record fields
	Service  string `json:"service,omitempty"`
	Proto    string `json:"proto,omitempty"`
	Name     string `json:"name,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Weight   int    `json:"weight,omitempty"`
	Port     int    `json:"port,omitempty"`
	Target   string `json:"target,omitempty"`

	// CAA record fields
	Flags int    `json:"flags,omitempty"`
	Tag   string `json:"tag,omitempty"`
	Value string `json:"value,omitempty"`
}

// QueryOptions contains options for DNS queries
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

// Response represents the result of a DNS operation
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Plan    *Plan        `json:"plan,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Plan represents a DNS modification plan
type Plan struct {
	Summary  string    `json:"summary"`
	Commands []Command `json:"commands"`
}

// Command represents a single DNS API command
type Command struct {
	Method   string `json:"method"`
	Endpoint string `json:"endpoint"`
	Body     string `json:"body,omitempty"`
	Reason   string `json:"reason"`
}

// QueryAnalysis contains the result of analyzing a DNS query
type QueryAnalysis struct {
	IsReadOnly   bool
	Operation    string // list, get, create, update, delete
	ResourceType string // zone, record
	ZoneName     string
	RecordName   string
	RecordType   string // A, AAAA, CNAME, MX, TXT, etc.
	RecordValue  string
	TTL          int
	Proxied      *bool
}
