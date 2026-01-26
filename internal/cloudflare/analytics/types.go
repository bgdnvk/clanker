package analytics

// ZoneAnalytics represents analytics data for a zone
type ZoneAnalytics struct {
	Requests  RequestsData  `json:"requests"`
	Bandwidth BandwidthData `json:"bandwidth"`
	Threats   ThreatsData   `json:"threats"`
	PageViews PageViewsData `json:"pageviews"`
	Uniques   UniquesData   `json:"uniques"`
}

// RequestsData contains request statistics
type RequestsData struct {
	All         int64            `json:"all"`
	Cached      int64            `json:"cached"`
	Uncached    int64            `json:"uncached"`
	ContentType map[string]int64 `json:"content_type,omitempty"`
	Country     map[string]int64 `json:"country,omitempty"`
	SSL         SSLStats         `json:"ssl,omitempty"`
	HTTPStatus  map[int]int64    `json:"http_status,omitempty"`
}

// SSLStats contains SSL/TLS statistics
type SSLStats struct {
	Encrypted   int64 `json:"encrypted"`
	Unencrypted int64 `json:"unencrypted"`
}

// BandwidthData contains bandwidth statistics
type BandwidthData struct {
	All      int64 `json:"all"`
	Cached   int64 `json:"cached"`
	Uncached int64 `json:"uncached"`
}

// ThreatsData contains threat statistics
type ThreatsData struct {
	All     int64            `json:"all"`
	Country map[string]int64 `json:"country,omitempty"`
	Type    map[string]int64 `json:"type,omitempty"`
}

// PageViewsData contains page view statistics
type PageViewsData struct {
	All int64 `json:"all"`
}

// UniquesData contains unique visitor statistics
type UniquesData struct {
	All int64 `json:"all"`
}

// QueryOptions contains options for analytics queries
type QueryOptions struct {
	ZoneID   string `json:"zone_id,omitempty"`
	ZoneName string `json:"zone_name,omitempty"`
	Since    string `json:"since,omitempty"` // -1440 for last 24 hours, etc.
	Until    string `json:"until,omitempty"`
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypeError  ResponseType = "error"
)

// Response represents the result of an analytics query
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// QueryAnalysis contains the result of analyzing an analytics query
type QueryAnalysis struct {
	ResourceType string // traffic, security, performance
	TimePeriod   string // 24h, 7d, 30d
	ZoneName     string
}
