package api

// The infrastructure half of v1: VPS instances, the catalog and orders that
// make them, operations, billing, firewalls, VPCs, DNS — and the endpoints
// being added (SSH keys, snapshots, backups, reverse DNS, …). Field names are
// the API's; unknown ones are ignored.

// Region is GET /v1/regions.
type Region struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	RegionName    *string `json:"region_name"`
	Available     bool    `json:"available"`
	IPv4Available bool    `json:"ipv4_available"`
	IPv6Available bool    `json:"ipv6_available"`
}

// TermRate is a price per hour on a billing term (µ¢ per hour).
type TermRate struct {
	Term                  string  `json:"term"`
	RateMicroCentsPerHour *string `json:"rate_micro_cents_per_hour"`
}

// Offering is a plan: GET /v1/catalog/offerings.
type Offering struct {
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description *string    `json:"description"`
	Kind        string     `json:"kind"`
	VCPUs       float64    `json:"vcpus"`
	RAMMB       float64    `json:"ram_mb"`
	BaseDiskGB  float64    `json:"base_disk_gb"`
	Rates       []TermRate `json:"rates"`
	Currency    string     `json:"currency"`
}

// CatalogImage is an operating system or an app: GET /v1/catalog/images.
type CatalogImage struct {
	Code             string           `json:"code"`
	Name             string           `json:"name"`
	OSFamily         string           `json:"os_family"`
	Type             string           `json:"type"`
	Category         string           `json:"category"`
	Description      *string          `json:"description"`
	SupportsPassword bool             `json:"supports_password"`
	MinDiskGB        *float64         `json:"min_disk_gb"`
	MinRAMMB         *float64         `json:"min_ram_mb"`
	MinVCPU          *float64         `json:"min_vcpu"`
	Inputs           []map[string]any `json:"inputs"`
}

// Addon is GET /v1/catalog/addons.
type Addon struct {
	Code            string     `json:"code"`
	Name            string     `json:"name"`
	Description     *string    `json:"description"`
	Type            string     `json:"type"`
	QuantityEnabled bool       `json:"quantity_enabled"`
	MinQuantity     float64    `json:"min_quantity"`
	MaxQuantity     *float64   `json:"max_quantity"`
	QuantityStep    float64    `json:"quantity_step"`
	Rates           []TermRate `json:"rates"`
	Currency        string     `json:"currency"`
}

type InstanceRegion struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	RegionName *string `json:"region_name"`
}

type InstanceSpecs struct {
	VCPUs  *float64 `json:"vcpus"`
	RAMMB  *float64 `json:"ram_mb"`
	DiskGB *float64 `json:"disk_gb"`
}

type InstanceIP struct {
	Address string `json:"address"`
	Version string `json:"version"`
	Primary bool   `json:"primary"`
}

// Instance is a VPS: GET /v1/instances/{id}.
type Instance struct {
	ID               string          `json:"id"`
	Name             *string         `json:"name"`
	Description      *string         `json:"description"`
	PowerStateCached string          `json:"power_state_cached"`
	Suspended        bool            `json:"suspended"`
	Region           *InstanceRegion `json:"region"`
	Specs            InstanceSpecs   `json:"specs"`
	Hostname         *string         `json:"hostname"`
	IPAddresses      []InstanceIP    `json:"ip_addresses"`
	VPCID            *string         `json:"vpc_id"`
	OfferingCode     *string         `json:"offering_code"`
	// Image is the catalog image it was built or last reinstalled from;
	// null for a custom image (CustomImageID).
	Image          *string `json:"image"`
	CustomImageID  *string `json:"custom_image_id"`
	ProjectID      *string `json:"project_id"`
	SubscriptionID *string `json:"subscription_id"`
	CreatedAt      *string `json:"created_at"`
}

type InstanceUsage struct {
	CPU              *float64 `json:"cpu"`
	MemoryUsedBytes  *float64 `json:"memory_used_bytes"`
	MemoryTotalBytes *float64 `json:"memory_total_bytes"`
	DiskUsedBytes    *float64 `json:"disk_used_bytes"`
	DiskTotalBytes   *float64 `json:"disk_total_bytes"`
	DiskState        *string  `json:"disk_state"`
}

// InstanceStatus is the live state: GET /v1/instances/{id}/status.
type InstanceStatus struct {
	InstanceID         string        `json:"instance_id"`
	PowerState         string        `json:"power_state"`
	Suspended          bool          `json:"suspended"`
	UptimeSeconds      *float64      `json:"uptime_seconds"`
	Usage              InstanceUsage `json:"usage"`
	Busy               bool          `json:"busy"`
	DeploymentProgress *float64      `json:"deployment_progress"`
	Cached             bool          `json:"cached"`
}

// TaskAccepted is a power action taken in: POST /v1/instances/{id}/power.
type TaskAccepted struct {
	InstanceID string  `json:"instance_id"`
	Action     string  `json:"action"`
	TaskID     *string `json:"task_id"`
}

// InstanceDeleted is DELETE /v1/instances/{id}.
type InstanceDeleted struct {
	InstanceID string `json:"instance_id"`
	Deleted    bool   `json:"deleted"`
	Message    string `json:"message"`
}

// OperationAccepted is what the newer long-running writes answer (202): the
// operation to wait on, when there is one.
type OperationAccepted struct {
	Object       string `json:"object"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	// InstanceID is the instance the work acts on: the one to poll when
	// there is no operation.
	InstanceID  string  `json:"instance_id"`
	OperationID *string `json:"operation_id"`
}

// Operation is GET /v1/operations/{id}: background work and how it went.
type Operation struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	ResourceType *string  `json:"resource_type"`
	ResourceID   *string  `json:"resource_id"`
	Progress     *float64 `json:"progress"`
	Error        *string  `json:"error"`
	StartedAt    *string  `json:"started_at"`
	CompletedAt  *string  `json:"completed_at"`
	CreatedAt    string   `json:"created_at"`
}

// Terminal says whether an operation has ended.
func (o Operation) Terminal() bool {
	return o.Status == "completed" || o.Status == "failed" || o.Status == "cancelled"
}

// Rescue mode.
type RescueMedia struct {
	Code              string  `json:"code"`
	Kind              string  `json:"kind"`
	Recommended       bool    `json:"recommended"`
	Available         bool    `json:"available"`
	UnavailableReason *string `json:"unavailable_reason"`
	SupportsSSH       bool    `json:"supports_ssh"`
	MinRAMMB          float64 `json:"min_ram_mb"`
}

type RescueHostKey struct {
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
}

type RescueAccess struct {
	Host         *string         `json:"host"`
	Username     *string         `json:"username"`
	SSH          bool            `json:"ssh"`
	PasswordAuth bool            `json:"password_auth"`
	SSHKeys      float64         `json:"ssh_keys"`
	HostKeys     []RescueHostKey `json:"host_keys"`
}

type RescueSession struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Since     string `json:"since"`
	Media     struct {
		Code    string `json:"code"`
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"media"`
	Ready        bool         `json:"ready"`
	Access       RescueAccess `json:"access"`
	AutoExitAt   *string      `json:"auto_exit_at"`
	ErrorCode    *string      `json:"error_code"`
	SnapshotName *string      `json:"snapshot_name"`
}

// RescueOptions is GET /v1/instances/{id}/rescue.
type RescueOptions struct {
	Available    bool          `json:"available"`
	Reason       *string       `json:"reason"`
	Hidden       bool          `json:"hidden"`
	Media        []RescueMedia `json:"media"`
	PowerState   string        `json:"power_state"`
	SSHReachable string        `json:"ssh_reachable"`
	Snapshot     struct {
		Offered   bool    `json:"offered"`
		FreeSlots float64 `json:"free_slots"`
		MaxSlots  float64 `json:"max_slots"`
	} `json:"snapshot"`
	AutoExitAfterHours     float64        `json:"auto_exit_after_hours"`
	ShutdownTimeoutSeconds float64        `json:"shutdown_timeout_seconds"`
	Session                *RescueSession `json:"session"`
}

// RescueEntered is POST /v1/instances/{id}/rescue (202).
type RescueEntered struct {
	SessionID    string  `json:"session_id"`
	State        string  `json:"state"`
	RootPassword *string `json:"root_password"`
	JobID        string  `json:"job_id"`
}

// RescueTransition is DELETE /v1/instances/{id}/rescue (202).
type RescueTransition struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	JobID     string `json:"job_id"`
}

type RescuePassword struct {
	RootPassword string `json:"root_password"`
}

// Orders: how an instance is bought.
type OrderImage struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	ImageID string `json:"image_id,omitempty"`
}

type QuoteLineItem struct {
	Label                     string   `json:"label"`
	Kind                      string   `json:"kind"`
	Quantity                  float64  `json:"quantity"`
	UnitRateMicroCentsPerHour string   `json:"unit_rate_micro_cents_per_hour"`
	RateMicroCentsPerHour     string   `json:"rate_micro_cents_per_hour"`
	CycleCents                *string  `json:"cycle_cents"`
	FreeQuantityApplied       *float64 `json:"free_quantity_applied"`
	RejectedReason            *string  `json:"rejected_reason"`
}

type QuoteViolation struct {
	Kind     string  `json:"kind"`
	Required float64 `json:"required"`
	Actual   float64 `json:"actual"`
	Remedy   string  `json:"remedy"`
}

// Quote is POST /v1/orders/quote.
type Quote struct {
	OfferingCode         string           `json:"offering_code"`
	Term                 string           `json:"term"`
	Purchasable          bool             `json:"purchasable"`
	Violations           []QuoteViolation `json:"violations"`
	HourlyRateMicroCents string           `json:"hourly_rate_micro_cents"`
	TermTotalCents       *string          `json:"term_total_cents"`
	Currency             string           `json:"currency"`
	LineItems            []QuoteLineItem  `json:"line_items"`
	Warnings             []string         `json:"warnings"`
}

type OrderPayment struct {
	InvoiceID                  string  `json:"invoice_id"`
	AmountCents                string  `json:"amount_cents"`
	Currency                   string  `json:"currency"`
	Status                     *string `json:"status"`
	PayURL                     *string `json:"pay_url"`
	DueAt                      *string `json:"due_at"`
	PaidAt                     *string `json:"paid_at"`
	BalanceAppliedCents        *string `json:"balance_applied_cents"`
	MinimumRechargeCreditCents *string `json:"minimum_recharge_credit_cents"`
}

// Order is POST /v1/orders and GET /v1/orders/{id}.
type Order struct {
	ID             *string       `json:"id"`
	Status         string        `json:"status"`
	SubscriptionID *string       `json:"subscription_id"`
	InstanceIDs    []string      `json:"instance_ids"`
	Payment        *OrderPayment `json:"payment"`
	Quote          *Quote        `json:"quote"`
	ExpiresAt      *string       `json:"expires_at"`
	CreatedAt      *string       `json:"created_at"`
}

// Billing.
type Balance struct {
	SubscriptionID string `json:"subscription_id"`
	AvailableCents string `json:"available_cents"`
	CommittedCents string `json:"committed_cents"`
	TemporaryCents string `json:"temporary_cents"`
	DebtCents      string `json:"debt_cents"`
	UsableCents    string `json:"usable_cents"`
	Currency       string `json:"currency"`
}

type Subscription struct {
	ID                   string   `json:"id"`
	Name                 *string  `json:"name"`
	Status               string   `json:"status"`
	CommitmentType       string   `json:"commitment_type"`
	Term                 *string  `json:"term"`
	CurrentCycleStartsAt *string  `json:"current_cycle_starts_at"`
	CurrentCycleEndsAt   *string  `json:"current_cycle_ends_at"`
	Balance              *Balance `json:"balance"`
	CreatedAt            *string  `json:"created_at"`
	Trial                *struct {
		TrialEndsAt *string `json:"trial_ends_at"`
		Ended       bool    `json:"ended"`
	} `json:"trial"`
}

type LedgerEntry struct {
	ID          string  `json:"id"`
	AmountCents string  `json:"amount_cents"`
	IsCredit    bool    `json:"is_credit"`
	Bucket      string  `json:"bucket"`
	Reason      string  `json:"reason"`
	ReferenceID *string `json:"reference_id"`
	OccurredAt  string  `json:"occurred_at"`
	Currency    string  `json:"currency"`
}

type TopUp struct {
	SubscriptionID string  `json:"subscription_id"`
	InvoiceID      string  `json:"invoice_id"`
	AmountCents    string  `json:"amount_cents"`
	Currency       string  `json:"currency"`
	PayURL         *string `json:"pay_url"`
	DueAt          *string `json:"due_at"`
}

type Invoice struct {
	ID             string  `json:"id"`
	ExternalID     *string `json:"external_id"`
	AmountCents    string  `json:"amount_cents"`
	Currency       string  `json:"currency"`
	Status         string  `json:"status"`
	Purpose        *string `json:"purpose"`
	Overdue        bool    `json:"overdue"`
	DueAt          *string `json:"due_at"`
	PaidAt         *string `json:"paid_at"`
	CreatedAt      *string `json:"created_at"`
	SubscriptionID *string `json:"subscription_id"`
}

// DNS.
type DNSZone struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt *string `json:"created_at"`
}

type DNSRecord struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	TTL            int      `json:"ttl"`
	Values         []string `json:"values"`
	DisabledValues []string `json:"disabled_values"`
}

// DNSHealth is GET /v1/dns/zones/{id}/health: whether the domain's
// registrar points at the platform's nameservers, looked up now.
type DNSHealth struct {
	ZoneID              string   `json:"zone_id"`
	Delegated           bool     `json:"delegated"`
	Nameservers         []string `json:"nameservers"`
	ExpectedNameservers []string `json:"expected_nameservers"`
}

// Firewalls.
type FirewallRule struct {
	ID          string  `json:"id"`
	Position    int     `json:"position"`
	Direction   string  `json:"direction"`
	Action      string  `json:"action"`
	Macro       *string `json:"macro"`
	Protocol    *string `json:"protocol"`
	DestPort    *string `json:"dest_port"`
	SourcePort  *string `json:"source_port"`
	Source      *string `json:"source"`
	Destination *string `json:"destination"`
	Enabled     bool    `json:"enabled"`
	Comment     *string `json:"comment"`
}

type Firewall struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Description       *string        `json:"description"`
	Enabled           bool           `json:"enabled"`
	HasPendingChanges bool           `json:"has_pending_changes"`
	PolicyIn          string         `json:"policy_in"`
	PolicyOut         string         `json:"policy_out"`
	Rules             []FirewallRule `json:"rules"`
	InstanceIDs       []string       `json:"instance_ids"`
	CreatedAt         *string        `json:"created_at"`
}

type FirewallMacro struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type FirewallCommit struct {
	FirewallID  string `json:"firewall_id"`
	OperationID string `json:"operation_id"`
}

// VPC is a private network between instances.
type VPC struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
	Subnet      *string     `json:"subnet"`
	Default     bool        `json:"default"`
	RegionID    *string     `json:"region_id"`
	InstanceIDs []string    `json:"instance_ids"`
	Members     []VPCMember `json:"members"`
	CreatedAt   *string     `json:"created_at"`
}

// VPCMember is an instance in a VPC: its private address is null until the
// private NIC is set up.
type VPCMember struct {
	InstanceID string  `json:"instance_id"`
	PrivateIP  *string `json:"private_ip"`
	Status     string  `json:"status"`
}

// ReverseDNS is one address's PTR record: GET /v1/reverse-dns.
type ReverseDNS struct {
	IP              string  `json:"ip"`
	Version         int     `json:"version"`
	Hostname        *string `json:"hostname"`
	DefaultHostname *string `json:"default_hostname"`
	InstanceID      *string `json:"instance_id"`
	// Editable: whether the PTR may change now (a new address may be held
	// a few days first); EditableAt says when, when not yet.
	Editable   *bool   `json:"editable"`
	EditableAt *string `json:"editable_at"`
}

// SSHKey is an account SSH key, put on instances at creation and reinstall (new).
type SSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	CreatedAt   string `json:"created_at"`
}

// Snapshot is an instance's point-in-time copy (new).
type Snapshot struct {
	ID          string  `json:"id"`
	InstanceID  string  `json:"instance_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IncludeRAM  bool    `json:"include_ram"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   string  `json:"expires_at"`
}

// SnapshotList is GET /v1/instances/{id}/snapshots (new): the slots too.
type SnapshotList struct {
	Data         []Snapshot `json:"data"`
	MaxSnapshots *int       `json:"max_snapshots"`
	Remaining    *int       `json:"remaining"`
}

// Backup is one of an instance's completed backups.
type Backup struct {
	ID         string  `json:"id"`
	InstanceID string  `json:"instance_id"`
	CreatedAt  *string `json:"created_at"`
}

// BackupList is GET /v1/instances/{id}/backups: the backups, newest first,
// whether one can be taken, the add-on's slots, and whether one is running.
type BackupList struct {
	Data           []Backup `json:"data"`
	Eligible       bool     `json:"eligible"`
	Reason         *string  `json:"reason"`
	Slots          int      `json:"slots"`
	AvailableSlots int      `json:"available_slots"`
	Running        bool     `json:"running"`
}

// BackupSchedule is an instance's automatic backups: the days and the time
// of day, in Timezone (UTC when null).
type BackupSchedule struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Days       []string `json:"days"`
	Time       string   `json:"time"`
	Timezone   *string  `json:"timezone"`
	LastRunAt  *string  `json:"last_run_at"`
}

// PasswordReset is POST /v1/instances/{id}/reset-password: the password
// only when the API generated it.
type PasswordReset struct {
	InstanceID string  `json:"instance_id"`
	Username   string  `json:"username"`
	Password   *string `json:"password"`
}

// MetricPoint is one step of an instance's metrics.
type MetricPoint struct {
	Time         string   `json:"time"`
	CPUPct       *float64 `json:"cpu_pct"`
	MemBytes     *float64 `json:"mem_bytes"`
	MemMaxBytes  *float64 `json:"mem_max_bytes"`
	DiskReadBps  *float64 `json:"disk_read_bps"`
	DiskWriteBps *float64 `json:"disk_write_bps"`
	NetInBps     *float64 `json:"net_in_bps"`
	NetOutBps    *float64 `json:"net_out_bps"`
}

// InstanceMetrics is GET /v1/instances/{id}/metrics (new).
type InstanceMetrics struct {
	InstanceID string        `json:"instance_id"`
	Timeframe  string        `json:"timeframe"`
	Points     []MetricPoint `json:"points"`
}

// BandwidthPoint is one step of an instance's traffic.
type BandwidthPoint struct {
	Time          string  `json:"time"`
	InboundBytes  float64 `json:"inbound_bytes"`
	OutboundBytes float64 `json:"outbound_bytes"`
}

// BandwidthCycle is the billing cycle's transfer, against what the plan includes.
type BandwidthCycle struct {
	StartsAt            string  `json:"starts_at"`
	EndsAt              string  `json:"ends_at"`
	IncludedGB          float64 `json:"included_gb"`
	PublicOutboundBytes float64 `json:"public_outbound_bytes"`
}

// InstanceBandwidth is GET /v1/instances/{id}/bandwidth.
type InstanceBandwidth struct {
	InstanceID         string           `json:"instance_id"`
	Period             string           `json:"period"`
	Bucket             string           `json:"bucket"`
	StartsAt           string           `json:"starts_at"`
	EndsAt             string           `json:"ends_at"`
	Series             []BandwidthPoint `json:"series"`
	TotalInboundBytes  float64          `json:"total_inbound_bytes"`
	TotalOutboundBytes float64          `json:"total_outbound_bytes"`
	Cycle              *BandwidthCycle  `json:"cycle"`
}

// ProjectResource is an instance, bucket or database put in a project.
type ProjectResource struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Detail  *string `json:"detail"`
	Image   *string `json:"image"`
	AddedAt *string `json:"added_at"`
}

// PresignedURL is a temporary link to upload (PUT) or download one object.
type PresignedURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// Resizing is by plan, the way DigitalOcean sizes and Vultr plans work: pick
// one of the offerings, price it (a quote changes nothing), apply it.

// ResizeOptions is GET /v1/instances/{id}/resize-options: what an instance
// is, and what it can become.
type ResizeOptions struct {
	InstanceID     string             `json:"instance_id"`
	Resizable      bool               `json:"resizable"`
	Reason         *string            `json:"reason"`
	CommitmentType string             `json:"commitment_type"`
	Term           *string            `json:"term"`
	CycleEndsAt    *string            `json:"cycle_ends_at"`
	Current        ResizeCurrent      `json:"current"`
	Capabilities   ResizeCapabilities `json:"capabilities"`
	Offerings      []ResizeOffering   `json:"offerings"`
	Addons         []ResizeAddon      `json:"addons"`
	IPAssignments  []ResizeIP         `json:"ip_assignments"`
	ExtraDisks     []ResizeDisk       `json:"extra_disks"`
	Limits         ResizeLimits       `json:"limits"`
	Currency       string             `json:"currency"`
}

type ResizeCurrent struct {
	OfferingCode         string  `json:"offering_code"`
	OfferingOnSale       bool    `json:"offering_on_sale"`
	VCPU                 float64 `json:"vcpu"`
	RAMMB                float64 `json:"ram_mb"`
	MainDiskGB           float64 `json:"main_disk_gb"`
	ExtraIPv4            float64 `json:"extra_ipv4"`
	ExtraIPv6            float64 `json:"extra_ipv6"`
	HourlyRateMicroCents string  `json:"hourly_rate_micro_cents"`
	RateIsGrandfathered  bool    `json:"rate_is_grandfathered"`
}

// ResizeCapabilities says which parts can change now.
type ResizeCapabilities struct {
	Addons   bool `json:"addons"`
	ExtraIPs bool `json:"extra_ips"`
	Storage  bool `json:"storage"`
}

type ResizeOffering struct {
	Code                 string  `json:"code"`
	Name                 string  `json:"name"`
	VCPU                 float64 `json:"vcpu"`
	RAMMB                float64 `json:"ram_mb"`
	MainDiskBaseGB       float64 `json:"main_disk_base_gb"`
	IncludedIPv4         float64 `json:"included_ipv4"`
	IncludedIPv6         float64 `json:"included_ipv6"`
	HourlyRateMicroCents string  `json:"hourly_rate_micro_cents"`
	Current              bool    `json:"current"`
}

// ResizeAddon is an add-on the instance can have. One whose quantity follows
// the vCPU count (a per-vCPU licence) is on or off, never a number.
type ResizeAddon struct {
	Code                 string   `json:"code"`
	Name                 string   `json:"name"`
	Type                 string   `json:"type"`
	CurrentQuantity      float64  `json:"current_quantity"`
	MinQuantity          float64  `json:"min_quantity"`
	MaxQuantity          *float64 `json:"max_quantity"`
	QuantityStep         float64  `json:"quantity_step"`
	QuantityFollowsVCPU  bool     `json:"quantity_follows_vcpu"`
	Removable            bool     `json:"removable"`
	HourlyRateMicroCents string   `json:"hourly_rate_micro_cents"`
}

type ResizeIP struct {
	AssignmentID string  `json:"assignment_id"`
	Version      int     `json:"version"`
	Address      *string `json:"address"`
	Primary      bool    `json:"primary"`
	AssignedAt   *string `json:"assigned_at"`
}

type ResizeDisk struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type ResizeLimits struct {
	MaxIPv4    float64 `json:"max_ipv4"`
	MaxIPv6    float64 `json:"max_ipv6"`
	CanAddIPv4 bool    `json:"can_add_ipv4"`
	CanAddIPv6 bool    `json:"can_add_ipv6"`
}

// ResizeQuote is POST /v1/instances/{id}/resize/quote: what a resize costs
// and whether it can be applied now.
type ResizeQuote struct {
	InstanceID                  string          `json:"instance_id"`
	SubscriptionID              *string         `json:"subscription_id"`
	HasChanges                  bool            `json:"has_changes"`
	CanApply                    bool            `json:"can_apply"`
	CurrentHourlyRateMicroCents string          `json:"current_hourly_rate_micro_cents"`
	NewHourlyRateMicroCents     string          `json:"new_hourly_rate_micro_cents"`
	NewCycleCents               *string         `json:"new_cycle_cents"`
	ChargeNowCents              string          `json:"charge_now_cents"`
	CreditCents                 string          `json:"credit_cents"`
	ShortfallCents              *string         `json:"shortfall_cents"`
	RequiresRestart             bool            `json:"requires_restart"`
	Removes                     []ResizeRemoved `json:"removes"`
	Violations                  []ResizeNotice  `json:"violations"`
	Warnings                    []ResizeNotice  `json:"warnings"`
	Currency                    string          `json:"currency"`
}

// ResizeRemoved is an address or disk a resize takes away, with its data.
type ResizeRemoved struct {
	Type    string `json:"type"`
	Label   string `json:"label"`
	Warning string `json:"warning"`
}

type ResizeNotice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResizeAccepted is the 202 of POST /v1/instances/{id}/resize: the billing
// has changed; the hypervisor work is OperationID.
type ResizeAccepted struct {
	OperationAccepted
	NewHourlyRateMicroCents *string        `json:"new_hourly_rate_micro_cents"`
	CreditCents             string         `json:"credit_cents"`
	Planned                 *ResizePlanned `json:"planned"`
}

type ResizePlanned struct {
	RequiresRestart bool    `json:"requires_restart"`
	MainDiskGrowGB  float64 `json:"main_disk_grow_gb"`
	DisksAdded      int     `json:"disks_added"`
	DisksRemoved    int     `json:"disks_removed"`
	IPv4Added       int     `json:"ipv4_added"`
	IPv4Removed     int     `json:"ipv4_removed"`
	IPv6Added       int     `json:"ipv6_added"`
	IPv6Removed     int     `json:"ipv6_removed"`
	RebootRequired  bool    `json:"reboot_required"`
}

// RemovalPreview is GET /v1/instances/{id}/removal-preview: what deleting
// an instance does to the bill, and what would make the API refuse it.
type RemovalPreview struct {
	Summary              string         `json:"summary"`
	Billing              string         `json:"billing"`
	SubscriptionID       *string        `json:"subscription_id"`
	HourlyRateMicroCents *string        `json:"hourly_rate_micro_cents"`
	Credit               *RemovalCredit `json:"credit"`
	UsableAfterCents     *string        `json:"usable_after_cents"`
	LastService          bool           `json:"last_service"`
	Blockers             []string       `json:"blockers"`
	Currency             string         `json:"currency"`
}

type RemovalCredit struct {
	AmountCents string  `json:"amount_cents"`
	ExpiresAt   *string `json:"expires_at"`
	NoneReason  *string `json:"none_reason"`
}

// CustomImage is one of the account's own images: a disk image an instance
// is built from, or an ISO.
type CustomImage struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Filename         string              `json:"filename"`
	Kind             string              `json:"kind"`
	Status           string              `json:"status"`
	SizeBytes        *string             `json:"size_bytes"`
	VirtualSizeBytes *string             `json:"virtual_size_bytes"`
	MinDiskGB        *float64            `json:"min_disk_gb"`
	Format           *string             `json:"format"`
	SHA256           *string             `json:"sha256"`
	RequiresEFI      bool                `json:"requires_efi"`
	RequiresTPM      bool                `json:"requires_tpm"`
	HasCloudInit     bool                `json:"has_cloud_init"`
	DefaultUsername  *string             `json:"default_username"`
	ExpiresAt        *string             `json:"expires_at"`
	FailureReason    *string             `json:"failure_reason"`
	Progress         *float64            `json:"progress"`
	InstanceCount    int                 `json:"instance_count"`
	Regions          []CustomImageRegion `json:"regions"`
	CreatedAt        string              `json:"created_at"`
}

// CustomImageRegion is where an image is ready to build from.
type CustomImageRegion struct {
	RegionID *string `json:"region_id"`
	State    string  `json:"state"`
}

// CustomImageQuota is GET /v1/images/quota: what the account may keep.
type CustomImageQuota struct {
	Images             int      `json:"images"`
	MaxImages          int      `json:"max_images"`
	TotalBytes         string   `json:"total_bytes"`
	MaxTotalBytes      string   `json:"max_total_bytes"`
	MaxFileBytes       string   `json:"max_file_bytes"`
	RetentionDays      int      `json:"retention_days"`
	UploadsEnabled     bool     `json:"uploads_enabled"`
	AcceptedExtensions []string `json:"accepted_extensions"`
}

// CustomImageUpload is an upload in parts: the parts still to send, each with
// its own presigned URL (PUT the bytes as they are, no headers).
type CustomImageUpload struct {
	ImageID       string            `json:"image_id"`
	PartSizeBytes int64             `json:"part_size_bytes"`
	PartCount     int               `json:"part_count"`
	UploadedBytes int64             `json:"uploaded_bytes"`
	Parts         []CustomImagePart `json:"parts"`
}

type CustomImagePart struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

// ActivityEntry is one line of GET /v1/activity: who did what to what.
type ActivityEntry struct {
	ID           string         `json:"id"`
	CreatedAt    string         `json:"created_at"`
	Action       string         `json:"action"`
	Category     string         `json:"category"`
	Significant  bool           `json:"significant"`
	Actor        ActivityActor  `json:"actor"`
	IP           *string        `json:"ip"`
	UserAgent    *string        `json:"user_agent"`
	Target       ActivityTarget `json:"target"`
	Outcome      string         `json:"outcome"`
	StatusCode   *int           `json:"status_code"`
	ErrorMessage *string        `json:"error_message"`
	RequestID    *string        `json:"request_id"`
}

type ActivityActor struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type ActivityTarget struct {
	Type  *string `json:"type"`
	ID    *string `json:"id"`
	Label *string `json:"label"`
}

// CloneCheck is POST /v1/instances/{id}/clone/quote: what copying one
// instance onto another does, and whether it can (it changes nothing).
type CloneCheck struct {
	OriginInstanceID      string            `json:"origin_instance_id"`
	DestinationInstanceID string            `json:"destination_instance_id"`
	CanClone              bool              `json:"can_clone"`
	Reason                *string           `json:"reason"`
	OriginDiskGB          *float64          `json:"origin_disk_gb"`
	DestinationDiskGB     *float64          `json:"destination_disk_gb"`
	DiskChanges           *CloneDiskChanges `json:"disk_changes"`
	Billing               *CloneBilling     `json:"billing"`
	Warnings              []string          `json:"warnings"`
	Violations            []ResizeNotice    `json:"violations"`
}

type CloneDiskChanges struct {
	MainDiskBeforeGB   float64   `json:"main_disk_before_gb"`
	MainDiskAfterGB    float64   `json:"main_disk_after_gb"`
	ExtraDisksBeforeGB []float64 `json:"extra_disks_before_gb"`
	ExtraDisksAfterGB  []float64 `json:"extra_disks_after_gb"`
}

type CloneBilling struct {
	CommitmentType              string  `json:"commitment_type"`
	SubscriptionID              string  `json:"subscription_id"`
	CurrentHourlyRateMicroCents string  `json:"current_hourly_rate_micro_cents"`
	NewHourlyRateMicroCents     string  `json:"new_hourly_rate_micro_cents"`
	ChargeNowCents              string  `json:"charge_now_cents"`
	CreditCents                 string  `json:"credit_cents"`
	ShortfallCents              *string `json:"shortfall_cents"`
}
