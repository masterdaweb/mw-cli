package api

// The Security Center: the account's score, the findings that lower it, the
// risks a person accepted, and each instance's scans. Field names are the
// API's.

// SeverityCounts is open findings by severity.
type SeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

// SecurityEngine is one part of the score: the exposure scan, TLS, known
// vulnerabilities, configuration checks, container images.
type SecurityEngine struct {
	Engine       string         `json:"engine"`
	Score        *float64       `json:"score"`
	Grade        *string        `json:"grade"`
	LastScanAt   *string        `json:"last_scan_at"`
	OpenFindings SeverityCounts `json:"open_findings"`
}

type SecurityInstanceCoverage struct {
	Total             int            `json:"total"`
	Assessed          int            `json:"assessed"`
	AwaitingFirstScan int            `json:"awaiting_first_scan"`
	Excluded          int            `json:"excluded"`
	OptedOut          int            `json:"opted_out"`
	ExcludedReasons   map[string]int `json:"excluded_reasons"`
}

// SecurityResourceCoverage is a kind of resource other than instances: how
// many there are and how many the checks have looked at.
type SecurityResourceCoverage struct {
	Type       string         `json:"type"`
	Total      int            `json:"total"`
	Checked    int            `json:"checked"`
	ImageScans map[string]int `json:"image_scans"`
}

type SecurityResourceScore struct {
	Type         string   `json:"type"`
	ID           string   `json:"id"`
	Name         *string  `json:"name"`
	Score        *float64 `json:"score"`
	Grade        *string  `json:"grade"`
	OpenCritical int      `json:"open_critical"`
	OpenHigh     int      `json:"open_high"`
}

// SecurityOverview is GET /v1/security/overview.
type SecurityOverview struct {
	Score             *float64                   `json:"score"`
	Grade             *string                    `json:"grade"`
	LastScanAt        *string                    `json:"last_scan_at"`
	ScanningAvailable bool                       `json:"scanning_available"`
	OpenFindings      SeverityCounts             `json:"open_findings"`
	Engines           []SecurityEngine           `json:"engines"`
	Instances         SecurityInstanceCoverage   `json:"instances"`
	Resources         []SecurityResourceCoverage `json:"resources"`
	WorstInstances    []SecurityResourceScore    `json:"worst_instances"`
	WorstResources    []SecurityResourceScore    `json:"worst_resources"`
}

type SecurityResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SecurityFinding is one item of GET /v1/security/findings; the detail (GET
// /v1/security/findings/{id}) adds References and Events.
type SecurityFinding struct {
	ID              string                 `json:"id"`
	Resource        SecurityResourceRef    `json:"resource"`
	RuleID          string                 `json:"rule_id"`
	Category        string                 `json:"category"`
	Severity        string                 `json:"severity"`
	Status          string                 `json:"status"`
	RiskScore       float64                `json:"risk_score"`
	KnownExploited  bool                   `json:"known_exploited"`
	CVEIDs          []string               `json:"cve_ids"`
	Port            *int                   `json:"port"`
	Protocol        *string                `json:"protocol"`
	Title           string                 `json:"title"`
	Description     string                 `json:"description"`
	Impact          string                 `json:"impact"`
	Recommendation  string                 `json:"recommendation"`
	FirstSeenAt     string                 `json:"first_seen_at"`
	LastSeenAt      string                 `json:"last_seen_at"`
	LastVerifiedAt  *string                `json:"last_verified_at"`
	ResolvedAt      *string                `json:"resolved_at"`
	OccurrenceCount int                    `json:"occurrence_count"`
	References      []string               `json:"references,omitempty"`
	Events          []SecurityFindingEvent `json:"events,omitempty"`
}

type SecurityFindingEvent struct {
	Type       string `json:"type"`
	OccurredAt string `json:"occurred_at"`
}

// SecurityException is a risk the account accepted: one finding, a check or
// a port, until it lapses or is revoked.
type SecurityException struct {
	ID            string                     `json:"id"`
	Scope         string                     `json:"scope"`
	Status        string                     `json:"status"`
	RuleID        *string                    `json:"rule_id"`
	Port          *int                       `json:"port"`
	Protocol      *string                    `json:"protocol"`
	InstanceID    *string                    `json:"instance_id"`
	Resource      *SecurityExceptionResource `json:"resource"`
	Justification string                     `json:"justification"`
	StartsAt      string                     `json:"starts_at"`
	ExpiresAt     string                     `json:"expires_at"`
	RevokedAt     *string                    `json:"revoked_at"`
}

// SecurityExceptionResource is what a finding exception was granted for,
// when that is not an instance.
type SecurityExceptionResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// SecuritySettings is GET/PATCH /v1/security/settings.
type SecuritySettings struct {
	Enabled             bool   `json:"enabled"`
	DefaultFrequency    string `json:"default_frequency"`
	NotifyOnNewCritical bool   `json:"notify_on_new_critical"`
	NotifyOnScoreDrop   bool   `json:"notify_on_score_drop"`
}

// InstanceSecurity is GET/PATCH /v1/instances/{id}/security.
type InstanceSecurity struct {
	InstanceID        string           `json:"instance_id"`
	State             string           `json:"state"`
	ExcludedReason    *string          `json:"excluded_reason"`
	Score             *float64         `json:"score"`
	Grade             *string          `json:"grade"`
	LastScanAt        *string          `json:"last_scan_at"`
	NextScanDueAt     *string          `json:"next_scan_due_at"`
	ScheduleFrequency *string          `json:"schedule_frequency"`
	OpenFindings      SeverityCounts   `json:"open_findings"`
	Engines           []SecurityEngine `json:"engines"`
	ScanningAvailable bool             `json:"scanning_available"`
}

// SecurityScan is one of an instance's scans.
type SecurityScan struct {
	ID               string   `json:"id"`
	InstanceID       string   `json:"instance_id"`
	Status           string   `json:"status"`
	Trigger          string   `json:"trigger"`
	QueuedAt         string   `json:"queued_at"`
	StartedAt        *string  `json:"started_at"`
	FinishedAt       *string  `json:"finished_at"`
	Score            *float64 `json:"score"`
	Grade            *string  `json:"grade"`
	FindingsNew      int      `json:"findings_new"`
	FindingsResolved int      `json:"findings_resolved"`
	FindingsReopened int      `json:"findings_reopened"`
}

// Terminal says whether a scan has ended (partial: some engines did not
// finish, the rest counts).
func (s SecurityScan) Terminal() bool {
	switch s.Status {
	case "completed", "partial", "failed", "cancelled", "skipped":
		return true
	}
	return false
}

// ISOAttachment is GET/POST /v1/instances/{id}/iso: what is in the
// instance's CD-ROM drive.
type ISOAttachment struct {
	InstanceID string  `json:"instance_id"`
	Attached   bool    `json:"attached"`
	ImageID    *string `json:"image_id"`
	ImageName  *string `json:"image_name"`
	AttachedAt *string `json:"attached_at"`
	EjectsAt   *string `json:"ejects_at"`
}
