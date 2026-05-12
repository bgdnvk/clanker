package flyio

// Domain types for the Fly.io REST + GraphQL surface. JSON tags match the
// wire shapes so the same structs can be used to unmarshal responses
// directly. Fields are intentionally permissive — Fly returns sparse JSON
// for many resources, and we don't want a missing optional to fail decoding.

// App is a Fly.io application (a logical container for machines, volumes,
// services, certs, IPs, secrets).
type App struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name"`
	Status          string `json:"status,omitempty"`
	Hostname        string `json:"hostname,omitempty"`
	AppURL          string `json:"app_url,omitempty"`
	PlatformVersion string `json:"platform_version,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	Network         string `json:"network,omitempty"`
	Organization    struct {
		ID   string `json:"id,omitempty"`
		Slug string `json:"slug,omitempty"`
	} `json:"organization,omitempty"`
}

// Machine is the Fly Machine — the per-VM primitive. State values include
// "started", "stopped", "suspended", "destroyed", "replacing", "created".
type Machine struct {
	ID         string         `json:"id"`
	Name       string         `json:"name,omitempty"`
	State      string         `json:"state,omitempty"`
	Region     string         `json:"region,omitempty"`
	ImageRef   ImageRef       `json:"image_ref,omitempty"`
	InstanceID string         `json:"instance_id,omitempty"`
	PrivateIP  string         `json:"private_ip,omitempty"`
	CreatedAt  string         `json:"created_at,omitempty"`
	UpdatedAt  string         `json:"updated_at,omitempty"`
	Config     *MachineConfig `json:"config,omitempty"`
	Checks     []MachineCheck `json:"checks,omitempty"`
	Events     []MachineEvent `json:"events,omitempty"`
	HostStatus string         `json:"host_status,omitempty"`
	Nonce      string         `json:"nonce,omitempty"`
}

// ImageRef is the OCI image identity for a machine.
type ImageRef struct {
	Registry   string            `json:"registry,omitempty"`
	Repository string            `json:"repository,omitempty"`
	Tag        string            `json:"tag,omitempty"`
	Digest     string            `json:"digest,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// MachineConfig is the per-machine launch config.
type MachineConfig struct {
	Image    string            `json:"image,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Services []MachineService  `json:"services,omitempty"`
	Mounts   []MachineMount    `json:"mounts,omitempty"`
	Guest    *MachineGuest     `json:"guest,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Init     *MachineInit      `json:"init,omitempty"`
	Schedule string            `json:"schedule,omitempty"`
	Restart  *MachineRestart   `json:"restart,omitempty"`
}

// MachineService is the public/private service exposed by a machine.
type MachineService struct {
	Protocol     string              `json:"protocol,omitempty"`
	InternalPort int                 `json:"internal_port,omitempty"`
	Ports        []MachinePort       `json:"ports,omitempty"`
	Concurrency  *MachineConcurrency `json:"concurrency,omitempty"`
	Checks       []MachineCheck      `json:"checks,omitempty"`
	Autostop     bool                `json:"autostop,omitempty"`
	Autostart    bool                `json:"autostart,omitempty"`
}

// MachinePort is a single port mapping inside a MachineService.
type MachinePort struct {
	Port              int      `json:"port,omitempty"`
	StartPort         int      `json:"start_port,omitempty"`
	EndPort           int      `json:"end_port,omitempty"`
	Handlers          []string `json:"handlers,omitempty"`
	ForceHTTPS        bool     `json:"force_https,omitempty"`
	TLSOptions        any      `json:"tls_options,omitempty"`
	HTTPOptions       any      `json:"http_options,omitempty"`
	ProxyProtoOptions any      `json:"proxy_proto_options,omitempty"`
}

// MachineConcurrency caps per-machine concurrency for the load balancer.
type MachineConcurrency struct {
	Type      string `json:"type,omitempty"`
	HardLimit int    `json:"hard_limit,omitempty"`
	SoftLimit int    `json:"soft_limit,omitempty"`
}

// MachineMount attaches a volume to a path on the machine.
type MachineMount struct {
	Volume    string `json:"volume,omitempty"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	SizeGB    int    `json:"size_gb,omitempty"`
}

// MachineGuest is the CPU/memory/GPU sizing.
type MachineGuest struct {
	CPUKind  string `json:"cpu_kind,omitempty"`
	CPUs     int    `json:"cpus,omitempty"`
	MemoryMB int    `json:"memory_mb,omitempty"`
	GPUKind  string `json:"gpu_kind,omitempty"`
}

// MachineCheck is one health check definition.
type MachineCheck struct {
	Name      string            `json:"name,omitempty"`
	Type      string            `json:"type,omitempty"`
	Port      int               `json:"port,omitempty"`
	Interval  string            `json:"interval,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
	GracePer  string            `json:"grace_period,omitempty"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Protocol  string            `json:"protocol,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Status    string            `json:"status,omitempty"`
	Output    string            `json:"output,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// MachineEvent is one entry in the machine state log.
type MachineEvent struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	Source    string `json:"source,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Request   any    `json:"request,omitempty"`
}

// MachineInit is the optional init/exec/entrypoint override block.
type MachineInit struct {
	Exec       []string `json:"exec,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	TTY        bool     `json:"tty,omitempty"`
}

// MachineRestart is the per-machine restart policy.
type MachineRestart struct {
	Policy     string `json:"policy,omitempty"`
	MaxRetries int    `json:"max_retries,omitempty"`
}

// Volume is a persistent volume backing a machine.
type Volume struct {
	ID                string `json:"id"`
	Name              string `json:"name,omitempty"`
	State             string `json:"state,omitempty"`
	Region            string `json:"region,omitempty"`
	Zone              string `json:"zone,omitempty"`
	SizeGB            int    `json:"size_gb,omitempty"`
	Encrypted         bool   `json:"encrypted,omitempty"`
	AttachedAllocID   string `json:"attached_alloc_id,omitempty"`
	AttachedMachineID string `json:"attached_machine_id,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	SnapshotRetention int    `json:"snapshot_retention,omitempty"`
	BlockSize         int    `json:"block_size,omitempty"`
	Blocks            int    `json:"blocks,omitempty"`
	BlocksFree        int    `json:"blocks_free,omitempty"`
	HostStatus        string `json:"host_status,omitempty"`
}

// VolumeSnapshot is a point-in-time snapshot of a volume.
type VolumeSnapshot struct {
	ID            string `json:"id"`
	Digest        string `json:"digest,omitempty"`
	Size          int64  `json:"size,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	RetentionDays int    `json:"retention_days,omitempty"`
	Status        string `json:"status,omitempty"`
}

// Secret is the name+digest record for one app secret. The value is never
// returned by Fly's API and never serialized through this type.
type Secret struct {
	Name      string `json:"name"`
	Digest    string `json:"digest,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// IPAddress is a Fly-allocated IP (v4 / v6 / shared_v4 / private_v6).
type IPAddress struct {
	ID        string `json:"id"`
	Address   string `json:"address,omitempty"`
	Type      string `json:"type,omitempty"`
	Region    string `json:"region,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Certificate represents a TLS certificate for a custom hostname.
type Certificate struct {
	ID                        string `json:"id,omitempty"`
	Hostname                  string `json:"hostname"`
	Configured                bool   `json:"configured,omitempty"`
	Source                    string `json:"source,omitempty"`
	Issued                    string `json:"issued,omitempty"`
	ClientStatus              string `json:"client_status,omitempty"`
	DNSProvider               string `json:"dns_provider,omitempty"`
	DNSValidationTarget       string `json:"dns_validation_target,omitempty"`
	DNSValidationHostname     string `json:"dns_validation_hostname,omitempty"`
	DNSValidationInstructions string `json:"dns_validation_instructions,omitempty"`
	AcmeDNSConfigured         bool   `json:"acme_dns_configured,omitempty"`
	CertificateAuthority      string `json:"certificate_authority,omitempty"`
	CreatedAt                 string `json:"created_at,omitempty"`
}

// Release is one app release entry (image deploy or restart).
type Release struct {
	ID          string `json:"id,omitempty"`
	Version     int    `json:"version,omitempty"`
	Stable      bool   `json:"stable,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Status      string `json:"status,omitempty"`
	ImageRef    string `json:"image_ref,omitempty"`
	User        string `json:"user,omitempty"`
}

// Organization is a Fly org (multi-tenancy boundary).
type Organization struct {
	ID        string `json:"id,omitempty"`
	Slug      string `json:"slug"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	PaidPlan  bool   `json:"paid_plan,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// OrgMember is one member entry for an organization.
type OrgMember struct {
	ID       string `json:"id,omitempty"`
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`
	JoinedAt string `json:"joined_at,omitempty"`
}

// User is the GraphQL `viewer` — the human who owns the token.
type User struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Region is a Fly platform region (data center).
type Region struct {
	Code             string  `json:"code"`
	Name             string  `json:"name,omitempty"`
	Latitude         float64 `json:"latitude,omitempty"`
	Longitude        float64 `json:"longitude,omitempty"`
	GatewayAvailable bool    `json:"gateway_available,omitempty"`
	RequiresPaidPlan bool    `json:"requires_paid_plan,omitempty"`
}

// Postgres is a Fly Postgres cluster — covers both the legacy unmanaged
// (Stolon-based) clusters and the newer managed MPG clusters. The `Managed`
// bool distinguishes them so callers can pick the right code path.
type Postgres struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Region          string   `json:"region,omitempty"`
	PrimaryRegion   string   `json:"primary_region,omitempty"`
	ReplicaRegions  []string `json:"replica_regions,omitempty"`
	Status          string   `json:"status,omitempty"`
	Plan            string   `json:"plan,omitempty"`
	Managed         bool     `json:"managed,omitempty"`
	ConnectionCount int      `json:"connection_count,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
}

// PostgresBackup is one backup of a managed Postgres cluster.
type PostgresBackup struct {
	ID        string `json:"id"`
	ClusterID string `json:"cluster_id,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Redis is a Fly Upstash Redis instance (addon).
type Redis struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Region        string   `json:"region,omitempty"`
	Status        string   `json:"status,omitempty"`
	Plan          string   `json:"plan,omitempty"`
	PrimaryRegion string   `json:"primary_region,omitempty"`
	ReadRegions   []string `json:"read_regions,omitempty"`
	Eviction      bool     `json:"eviction,omitempty"`
}

// Tigris is a Fly Tigris object storage bucket (addon).
type Tigris struct {
	ID           string `json:"id"`
	BucketName   string `json:"bucket_name,omitempty"`
	Region       string `json:"region,omitempty"`
	PublicAccess bool   `json:"public_access,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// MySQL is a Fly managed MySQL instance (addon).
type MySQL struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Region    string `json:"region,omitempty"`
	Status    string `json:"status,omitempty"`
	Plan      string `json:"plan,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// WireGuardPeer is a member of the org's 6PN private network.
type WireGuardPeer struct {
	Name      string `json:"name"`
	Region    string `json:"region,omitempty"`
	PeerIP    string `json:"peer_ip,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	OrgSlug   string `json:"org_slug,omitempty"`
}

// AuthToken is a Fly API token (deploy / read-only / org / etc.).
type AuthToken struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	Type       string `json:"type,omitempty"`
}

// Extension is a Fly marketplace add-on (Sentry, Tigris, etc.).
type Extension struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	OrgSlug     string `json:"org_slug,omitempty"`
}

// ScaleConfig captures a scaling intent (count + sizing) for one app.
type ScaleConfig struct {
	AppName  string `json:"app_name"`
	Region   string `json:"region,omitempty"`
	Count    int    `json:"count,omitempty"`
	Preset   string `json:"preset,omitempty"`
	MemoryMB int    `json:"memory_mb,omitempty"`
	CPUKind  string `json:"cpu_kind,omitempty"`
	CPUs     int    `json:"cpus,omitempty"`
}

// UsageSummary is the synthesized usage rollup for billing/cost estimation.
type UsageSummary struct {
	MachineHours  map[string]float64 `json:"machine_hours,omitempty"`
	VolumeGBHours float64            `json:"volume_gb_hours,omitempty"`
	BandwidthGB   float64            `json:"bandwidth_gb,omitempty"`
	LogLines      int64              `json:"log_lines,omitempty"`
	Period        string             `json:"period,omitempty"`
}
