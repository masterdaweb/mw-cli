package api

import (
	"encoding/json"
	"fmt"
)

// The parts of the v1 answers the CLI reads. Unknown fields are ignored, so a
// newer API never breaks an older CLI.

// Me is GET /v1/me: the account the token belongs to, and what it may do.
type Me struct {
	AccountID string   `json:"account_id"`
	TokenID   string   `json:"token_id"`
	Scopes    []string `json:"scopes"`
}

type Project struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Region      string  `json:"region"`
	// DatacenterID is where it runs, for good: a region's id (`mw regions`).
	DatacenterID       *int64 `json:"datacenter_id"`
	Deleting           bool   `json:"deleting"`
	DeletionProtection bool   `json:"deletion_protection"`
	DeploysFrozen      bool   `json:"deploys_frozen"`
	Paused             bool   `json:"paused"`
	// PreviewsEnabled: each pull request against a branch it deploys gets a copy.
	PreviewsEnabled bool            `json:"previews_enabled"`
	Preview         *ProjectPreview `json:"preview"`
	ServiceCount    int             `json:"service_count"`
	LiveCount       int             `json:"live_count"`
	FailedCount     int             `json:"failed_count"`
	// What else it groups: instances (VPS), databases and buckets.
	InstanceCount int    `json:"instance_count"`
	DatabaseCount int    `json:"database_count"`
	BucketCount   int    `json:"bucket_count"`
	CreatedAt     string `json:"created_at"`
}

// ProjectPreview: this project is a pull request's preview environment.
type ProjectPreview struct {
	OfProjectID *string `json:"of_project_id"`
	Number      int     `json:"number"`
	Title       *string `json:"title"`
	URL         *string `json:"url"`
	Branch      string  `json:"branch"`
}

// PreviewEnvironment is one open pull request's copy of a project.
type PreviewEnvironment struct {
	Project  Project `json:"project"`
	Services []struct {
		Name   string  `json:"name"`
		Kind   string  `json:"kind"`
		Status string  `json:"status"`
		URL    *string `json:"url"`
	} `json:"services"`
}

type ServiceSettings struct {
	Size           *string `json:"size"`
	VCPUMillis     int     `json:"vcpu_millis"`
	MemoryMiB      int     `json:"memory_mib"`
	StartCommand   *string `json:"start_command"`
	ReleaseCommand *string `json:"release_command"`
	Port           int     `json:"port"`
	MinInstances   int     `json:"min_instances"`
	MaxInstances   int     `json:"max_instances"`
	Concurrency    int     `json:"concurrency"`
	Instances      int     `json:"instances"`
	CronSchedule   *string `json:"cron_schedule"`
	// RolloutPercent: a new version's first share of the requests; nil, all at once.
	RolloutPercent *int `json:"rollout_percent"`
	RolloutMinutes int  `json:"rollout_minutes"`
	// The persistent volume: where it is mounted and its size; nil, none.
	VolumeMountPath *string `json:"volume_mount_path"`
	VolumeSizeGB    *int    `json:"volume_size_gb"`
}

type DeploymentSummary struct {
	ID            string  `json:"id"`
	Number        int     `json:"number"`
	Status        string  `json:"status"`
	Trigger       string  `json:"trigger"`
	CommitSHA     *string `json:"commit_sha"`
	CommitMessage *string `json:"commit_message"`
	CreatedAt     string  `json:"created_at"`
	FinishedAt    *string `json:"finished_at"`
}

type ServiceSource struct {
	// GIT, IMAGE or UPLOAD (what `mw deploy` sends from a folder).
	Kind string `json:"kind"`
	Git  *struct {
		Provider     string  `json:"provider"`
		RepoFullName *string `json:"repo_full_name"`
		RepoURL      *string `json:"repo_url"`
		Branch       *string `json:"branch"`
	} `json:"git"`
	Image *struct {
		Ref string `json:"ref"`
	} `json:"image"`
}

// Access is GET /v1/serverless/access: whether the account may use
// serverless, and its limits.
type Access struct {
	Enabled        bool    `json:"enabled"`
	Beta           bool    `json:"beta"`
	Reason         *string `json:"reason"`
	UploadMaxBytes *int64  `json:"upload_max_bytes"`
}

type Service struct {
	ID              string             `json:"id"`
	ProjectID       string             `json:"project_id"`
	ProjectName     string             `json:"project_name"`
	Name            string             `json:"name"`
	Kind            string             `json:"kind"`
	Status          string             `json:"status"`
	StatusReason    *string            `json:"status_reason"`
	Source          ServiceSource      `json:"source"`
	Settings        ServiceSettings    `json:"settings"`
	URL             *string            `json:"url"`
	InternalURL     *string            `json:"internal_url"`
	ReadyInstances  int                `json:"ready_instances"`
	LastDeployment  *DeploymentSummary `json:"last_deployment"`
	DeletionProtect bool               `json:"deletion_protection"`
	// VolumeUsage: how full the volume was when last measured; nil, not yet.
	VolumeUsage *VolumeUsage `json:"volume_usage"`
	// ImageVolumes: the paths its image keeps data in (its VOLUME lines).
	ImageVolumes []string `json:"image_volumes"`
	CreatedAt    string   `json:"created_at"`
}

// VolumeUsage is a volume's file system: bytes used and its size, decimal strings.
type VolumeUsage struct {
	UsedBytes     string `json:"used_bytes"`
	CapacityBytes string `json:"capacity_bytes"`
	MeasuredAt    string `json:"measured_at"`
}

// VolumeBackup is one backup of a service's volume. Kind is DAILY (kept 7
// days), MANUAL (kept until deleted), BEFORE_RESTORE (what a restore
// replaced) or FINAL (made when the volume went); ExpiresAt nil, kept until
// deleted.
type VolumeBackup struct {
	ID           string  `json:"id"`
	ServiceID    string  `json:"service_id"`
	ServiceName  string  `json:"service_name"`
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	MountPath    string  `json:"mount_path"`
	VolumeSizeGB int     `json:"volume_size_gb"`
	SizeBytes    *string `json:"size_bytes"`
	RequestedBy  *string `json:"requested_by"`
	CreatedAt    string  `json:"created_at"`
	CompletedAt  *string `json:"completed_at"`
	ExpiresAt    *string `json:"expires_at"`
	Restorable   bool    `json:"restorable"`
}

// VolumeEntry is one file or folder: Type is file, dir, link or other.
type VolumeEntry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
}

// VolumeFiles is a folder of a volume ("" is its root); Truncated, it holds
// more than is listed.
type VolumeFiles struct {
	Path      string        `json:"path"`
	Entries   []VolumeEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

// ImageInspection is what an image says of itself: the ports it exposes, the
// paths it keeps data in, and the user it runs as (nil: root).
type ImageInspection struct {
	Ports   []int    `json:"ports"`
	Volumes []string `json:"volumes"`
	User    *string  `json:"user"`
}

type DeploymentFailure struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Hint    *string `json:"hint"`
	LogTail *string `json:"log_tail"`
}

type DeploymentStep struct {
	Kind    string  `json:"kind"`
	Status  string  `json:"status"`
	Message *string `json:"message"`
}

type Deployment struct {
	ID            string             `json:"id"`
	Number        int                `json:"number"`
	ServiceID     string             `json:"service_id"`
	Status        string             `json:"status"`
	Trigger       string             `json:"trigger"`
	CommitSHA     *string            `json:"commit_sha"`
	CommitMessage *string            `json:"commit_message"`
	ImageRef      *string            `json:"image_ref"`
	RevisionID    *string            `json:"revision_id"`
	Failure       *DeploymentFailure `json:"failure"`
	Rollout       *DeploymentRollout `json:"rollout"`
	Steps         []DeploymentStep   `json:"steps"`
	CreatedAt     string             `json:"created_at"`
	FinishedAt    *string            `json:"finished_at"`
}

// DeploymentRollout: a gradual rollout — the share of the requests the new
// version took, until when (still, while the deployment is DEPLOYING).
type DeploymentRollout struct {
	Percent       int     `json:"percent"`
	StartedAt     string  `json:"started_at"`
	Until         string  `json:"until"`
	PendingAction *string `json:"pending_action"`
}

// BuildLog is what a deployment's build printed: `running` (its last lines
// so far), `complete` (kept), or `none`.
type BuildLog struct {
	DeploymentID string `json:"deployment_id"`
	State        string `json:"state"`
	Log          string `json:"log"`
	Truncated    bool   `json:"truncated"`
}

type Revision struct {
	ID                string  `json:"id"`
	Number            int     `json:"number"`
	Status            string  `json:"status"`
	Live              bool    `json:"live"`
	RollbackAvailable bool    `json:"rollback_available"`
	CommitSHA         *string `json:"commit_sha"`
	Image             *string `json:"image"`
	Size              *string `json:"size"`
	CreatedAt         string  `json:"created_at"`
}

// Variable: a secret's value is never returned (`value` is null).
type Variable struct {
	Key              string  `json:"key"`
	Value            *string `json:"value"`
	IsSecret         bool    `json:"is_secret"`
	AvailableAtBuild bool    `json:"available_at_build"`
	UpdatedAt        string  `json:"updated_at"`
}

// VariableChange is what setting, importing or removing variables did.
type VariableChange struct {
	Created    []string    `json:"created"`
	Updated    []string    `json:"updated"`
	Removed    []string    `json:"removed"`
	Unchanged  []string    `json:"unchanged"`
	Deployment *Deployment `json:"deployment"`
}

// DomainInstruction is a DNS record to create. `found` says whether the last
// check saw it; `option` groups the alternatives for a root domain.
type DomainInstruction struct {
	Type   string  `json:"type"`
	Name   string  `json:"name"`
	Value  string  `json:"value"`
	Found  *bool   `json:"found"`
	Option *string `json:"option"`
}

type DomainError struct {
	Code    string  `json:"code"`
	Message *string `json:"message"`
}

type Domain struct {
	ID           string              `json:"id"`
	Hostname     string              `json:"hostname"`
	Status       string              `json:"status"`
	IsDefault    bool                `json:"is_default"`
	Apex         bool                `json:"apex"`
	RedirectTo   *string             `json:"redirect_to"`
	ViaRedirect  bool                `json:"via_redirect"`
	Instructions []DomainInstruction `json:"instructions"`
	Error        *DomainError        `json:"error"`
	ActivatedAt  *string             `json:"activated_at"`
	CreatedAt    string              `json:"created_at"`
}

// Binding is a bucket or database a service reads as variables (their names
// only: never a credential).
type Binding struct {
	ID                  string   `json:"id"`
	ResourceKind        string   `json:"resource_kind"`
	ResourceID          string   `json:"resource_id"`
	ResourceName        *string  `json:"resource_name"`
	EnvProfile          string   `json:"env_profile"`
	EnvPrefix           string   `json:"env_prefix"`
	Access              string   `json:"access"`
	Variables           []string `json:"variables"`
	CredentialRotatedAt *string  `json:"credential_rotated_at"`
	CreatedAt           string   `json:"created_at"`
}

type BindingChange struct {
	Binding    *Binding    `json:"binding"`
	Deployment *Deployment `json:"deployment"`
}

type LogLine struct {
	TS       string `json:"ts"`
	Instance string `json:"instance"`
	Revision int    `json:"revision"`
	Message  string `json:"message"`
}

// Logs is GET /v1/serverless/services/{id}/logs.
type Logs struct {
	Lines     []LogLine `json:"lines"`
	Truncated bool      `json:"truncated"`
}

// Wallet is the serverless credit every product here is paid from.
type Wallet struct {
	SubscriptionID       string  `json:"subscription_id"`
	Status               string  `json:"status"`
	UsableMicroCents     string  `json:"usable_micro_cents"`
	MinimumForNewService string  `json:"minimum_for_new_service_micro_cents"`
	DebtLimitMicroCents  string  `json:"debt_limit_micro_cents"`
	SuspendedAt          *string `json:"suspended_at"`
	RunwayHours          *int    `json:"runway_hours"`
	// While suspended: from when the data may be deleted if the credit stays out.
	RetentionDeleteAt *string `json:"retention_delete_at"`
	// A top-up invoice on its own at three days of credit left, of this much.
	AutoRechargeEnabled bool    `json:"auto_recharge_enabled"`
	AutoRechargeAmount  *string `json:"auto_recharge_amount_micro_cents"`
}

// SpendCap is the most serverless may cost a period, and what reaching it does.
type SpendCap struct {
	CapMicroCents   *string `json:"cap_micro_cents"`
	Action          string  `json:"action"`
	SpentMicroCents string  `json:"spent_micro_cents"`
	PeriodFrom      string  `json:"period_from"`
	PeriodTo        string  `json:"period_to"`
	Stopped         bool    `json:"stopped"`
}

type ServiceUsage struct {
	ServiceID       string  `json:"service_id"`
	Name            *string `json:"name"`
	InstanceSeconds int64   `json:"instance_seconds"`
	JobSeconds      int64   `json:"job_seconds"`
	BuildSeconds    int64   `json:"build_seconds"`
	CostMicroCents  string  `json:"cost_micro_cents"`
}

// Usage is GET /v1/serverless/usage: the wallet's current period.
type Usage struct {
	PeriodFrom            string         `json:"period_from"`
	PeriodTo              string         `json:"period_to"`
	Priced                bool           `json:"priced"`
	TotalMicroCents       string         `json:"total_micro_cents"`
	ForecastMicroCents    string         `json:"forecast_micro_cents"`
	BuildSeconds          int64          `json:"build_seconds"`
	BuildAllowanceMinutes int64          `json:"build_allowance_minutes"`
	Services              []ServiceUsage `json:"services"`
}

type StorageUsage struct {
	PeriodFrom     string `json:"period_from"`
	PeriodTo       string `json:"period_to"`
	Priced         bool   `json:"priced"`
	CostMicroCents string `json:"cost_micro_cents"`
}

type DatabaseSize struct {
	Code              string  `json:"code"`
	CPUs              int     `json:"cpus"`
	MemoryMB          int     `json:"memory_mb"`
	DiskGB            int     `json:"disk_gb"`
	MaxConnections    *int    `json:"max_connections"`
	MaxMemoryMB       *int    `json:"max_memory_mb"`
	HourlyMicroCents  *string `json:"hourly_micro_cents"`
	MonthlyMicroCents *string `json:"monthly_micro_cents"`
}

type DatabaseConnection struct {
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	PoolerPort *int    `json:"pooler_port"`
	Database   *string `json:"database"`
	Username   string  `json:"username"`
	URI        string  `json:"uri"`
	DirectURI  *string `json:"direct_uri"`
}

type DatabaseHealth struct {
	State          string  `json:"state"`
	Reporting      bool    `json:"reporting"`
	ServerVersion  *string `json:"server_version"`
	Connections    *int    `json:"connections"`
	MaxConnections *int    `json:"max_connections"`
	DiskUsedBytes  *string `json:"disk_used_bytes"`
	DiskTotalBytes *string `json:"disk_total_bytes"`
}

type DatabaseBackups struct {
	LastBackupAt      *string                `json:"last_backup_at"`
	EarliestRestoreAt *string                `json:"earliest_restore_at"`
	RestorePoints     []DatabaseRestorePoint `json:"restore_points"`
}

// DatabaseRestorePoint is a named instant a restore can stop at.
type DatabaseRestorePoint struct {
	Name             string  `json:"name"`
	Reason           string  `json:"reason"`
	ServiceName      *string `json:"service_name"`
	DeploymentNumber *int    `json:"deployment_number"`
	CreatedAt        string  `json:"created_at"`
}

// DatabaseRestore is what a database was restored from.
type DatabaseRestore struct {
	SourceName *string `json:"source_name"`
	Target     string  `json:"target"`
	Time       *string `json:"time"`
	Point      *string `json:"point"`
	RestoredAt *string `json:"restored_at"`
}

// DatabaseHibernation is a database asleep (or going to sleep, or waking).
type DatabaseHibernation struct {
	Since             *string `json:"since"`
	BackupBytes       string  `json:"backup_bytes"`
	MonthlyMicroCents *string `json:"monthly_micro_cents"`
	Waking            bool    `json:"waking"`
}

// DatabaseMaintenance is its weekly window, and what waits for it.
type DatabaseMaintenance struct {
	Day             *int    `json:"day"`
	HourUTC         int     `json:"hour_utc"`
	NextAt          string  `json:"next_at"`
	ScheduledSize   *string `json:"scheduled_size"`
	ScheduledDiskGB *int    `json:"scheduled_disk_gb"`
	PendingUpdate   *string `json:"pending_update"`
}

// DatabaseDisk is its disk, and how far it grows by itself.
type DatabaseDisk struct {
	SizeGB        int     `json:"size_gb"`
	UsedBytes     *string `json:"used_bytes"`
	TotalBytes    *string `json:"total_bytes"`
	AutogrowMaxGB *int    `json:"autogrow_max_gb"`
}

// DatabaseJob is an export (a dump to download) or an import.
type DatabaseJob struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	Format    *string `json:"format"`
	SizeBytes *string `json:"size_bytes"`
	Source    *string `json:"source"`
	Replace   *bool   `json:"replace"`
	// Database is what an import restores as (SQL Server), or which of its
	// databases an export holds.
	Database  *string `json:"database"`
	ErrorCode *string `json:"error_code"`
	Error     *string `json:"error"`
	Report    *struct {
		Tables                  int      `json:"tables"`
		LeftOut                 int      `json:"left_out"`
		Notes                   []string `json:"notes"`
		DefinersRemoved         int      `json:"definers_removed"`
		ServerStatementsRemoved int      `json:"server_statements_removed"`
		OtherDatabase           *string  `json:"other_database"`
	} `json:"report"`
	CreatedAt  string  `json:"created_at"`
	FinishedAt *string `json:"finished_at"`
	ExpiresAt  *string `json:"expires_at"`
}

// DatabaseExportLink downloads a dump, for a few minutes.
type DatabaseExportLink struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
	Filename  string `json:"filename"`
}

type Database struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Engine             string             `json:"engine"`
	Version            Series             `json:"version"`
	Size               DatabaseSize       `json:"size"`
	Status             string             `json:"status"`
	Failure            *string            `json:"failure"`
	ChangesPending     bool               `json:"changes_pending"`
	Connection         DatabaseConnection `json:"connection"`
	AllowedCIDRs       []string           `json:"allowed_cidrs"`
	Extensions         []string           `json:"extensions"`
	EvictionPolicy     *string            `json:"eviction_policy"`
	BackupsEnabled     bool               `json:"backups_enabled"`
	DeletionProtection bool               `json:"deletion_protection"`
	ProjectID          *string            `json:"project_id"`
	// DatacenterID is where it runs, for good: a region's id (`mw regions`).
	DatacenterID *int64               `json:"datacenter_id"`
	Health       DatabaseHealth       `json:"health"`
	Backups      *DatabaseBackups     `json:"backups"`
	Notice       *string              `json:"notice"`
	Restore      *DatabaseRestore     `json:"restore"`
	Hibernation  *DatabaseHibernation `json:"hibernation"`
	Maintenance  DatabaseMaintenance  `json:"maintenance"`
	Disk         DatabaseDisk         `json:"disk"`
	Services     []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"services"`
	Cost struct {
		MonthToDateMicroCents string `json:"month_to_date_micro_cents"`
	} `json:"cost"`
	// ExtraDatabases are the databases it holds besides its own (SQL
	// Server's imports, or ones made in the console).
	ExtraDatabases []string `json:"extra_databases"`
	// SQL Server: the server's collation (fixed at creation) and licence.
	Collation *string          `json:"collation"`
	Licence   *DatabaseLicence `json:"licence"`
	// Warnings are what the owner should act on (none: nothing).
	Warnings   []DatabaseWarning `json:"warnings"`
	CreatedAt  string            `json:"created_at"`
	DeletedAt  *string           `json:"deleted_at"`
	PurgeAfter *string           `json:"purge_after"`
}

// DatabaseLicence is a SQL Server database's edition and licence: never the
// product key itself.
type DatabaseLicence struct {
	Edition       string `json:"edition"`
	HasProductKey bool   `json:"has_product_key"`
	// RunningEdition is what the server reports running ("" while it is
	// down; null before it reported).
	RunningEdition *string `json:"running_edition"`
	// KeyApplied: the recorded key is in force (null for Express, or before
	// the server reported).
	KeyApplied *bool `json:"key_applied"`
	// Problem is why the edition is not the one asked for, in SQL Server's
	// words (the key it refused).
	Problem *string `json:"problem"`
}

// DatabaseWarning is something the owner should act on: a stable code, and
// the API's sentence for it.
type DatabaseWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type DatabaseEngineInfo struct {
	Engine           string         `json:"engine"`
	Versions         []Series       `json:"versions"`
	Sizes            []DatabaseSize `json:"sizes"`
	Extensions       []string       `json:"extensions"`
	EvictionPolicies []string       `json:"eviction_policies"`
}

type DatabasesInfo struct {
	Available                   bool                 `json:"available"`
	Engines                     []DatabaseEngineInfo `json:"engines"`
	RetentionDays               int                  `json:"retention_days"`
	DatabaseLimit               int                  `json:"database_limit"`
	CertificatePublic           bool                 `json:"certificate_public"`
	HibernatedGBMonthMicroCents *string              `json:"hibernated_gb_month_micro_cents"`
}

// DatabaseTunnel is one TCP connection to a database through the platform:
// a WebSocket carrying it, once, before ExpiresAt. TLS stays between the
// client and the database, verified against Hostname.
type DatabaseTunnel struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
	Hostname  string `json:"hostname"`
	Port      int    `json:"port"`
	// FingerprintSHA256 is the database certificate's, for a client that
	// cannot check the name through the tunnel (MariaDB's --ssl-fp).
	FingerprintSHA256 *string `json:"fingerprint_sha256"`
}

// Run is a one-off command (or a shell) in a service's live revision.
type Run struct {
	ID         string  `json:"id"`
	ServiceID  string  `json:"service_id"`
	Command    *string `json:"command"`
	TTY        bool    `json:"tty"`
	Status     string  `json:"status"`
	ExitCode   *int    `json:"exit_code"`
	Error      *string `json:"error"`
	ActorKind  string  `json:"actor_kind"`
	ActorLabel *string `json:"actor_label"`
	CreatedAt  string  `json:"created_at"`
	AttachedAt *string `json:"attached_at"`
	EndedAt    *string `json:"ended_at"`
}

// StartedRun is a run and the WebSocket that attaches to it, once.
type StartedRun struct {
	Run       Run    `json:"run"`
	AttachURL string `json:"attach_url"`
	ExpiresAt string `json:"expires_at"`
}

// DatabaseCA is the CA database certificates chain to; null when the
// system's own roots verify them.
type DatabaseCA struct {
	CACertificate *string `json:"ca_certificate"`
}

type CreatedDatabase struct {
	Database Database `json:"database"`
	// Null for a developer: the password stays with the account's administrators.
	Password *string `json:"password"`
}

type Password struct {
	Password string `json:"password"`
}

type Bucket struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Location           string  `json:"location"`
	Public             bool    `json:"public"`
	PublicURL          *string `json:"public_url"`
	DeletionProtection bool    `json:"deletion_protection"`
	QuotaBytes         *string `json:"quota_bytes"`
	QuotaMode          string  `json:"quota_mode"`
	SizeBytes          string  `json:"size_bytes"`
	Objects            string  `json:"object_count"`
	Status             string  `json:"status"`
	DeletedAt          *string `json:"deleted_at"`
	PurgeAfter         *string `json:"purge_after"`
	CreatedAt          string  `json:"created_at"`
}

type StorageInfo struct {
	Available   bool    `json:"available"`
	Endpoint    string  `json:"endpoint"`
	Region      string  `json:"region"`
	Location    *string `json:"location"`
	BucketLimit int     `json:"bucket_limit"`
	KeyLimit    int     `json:"key_limit"`
	RestoreDays int     `json:"restore_days"`
}

type Object struct {
	Key          string  `json:"key"`
	Size         int64   `json:"size"`
	LastModified *string `json:"last_modified"`
}

// ObjectListing is one page of a bucket, by folder (`prefixes`).
type ObjectListing struct {
	Prefixes []string `json:"prefixes"`
	Objects  []Object `json:"objects"`
	Cursor   *string  `json:"cursor"`
}

type AccessKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	AccessKeyID string   `json:"access_key_id"`
	Access      string   `json:"access"`
	Buckets     []string `json:"buckets"`
	ExpiresAt   *string  `json:"expires_at"`
	LastUsedAt  *string  `json:"last_used_at"`
	CreatedAt   string   `json:"created_at"`
}

// NewAccessKey: the secret is in this answer and nowhere else, ever.
type NewAccessKey struct {
	Key             AccessKey `json:"key"`
	SecretAccessKey string    `json:"secret_access_key"`
}

// Series is a database's release series ("18", "8.4", "11.8"). The API sent
// whole numbers before MySQL's series made them strings; both are read, so a
// CLI and an API of different ages still understand each other.
type Series string

// UnmarshalJSON takes "8.4" or 18.
func (s *Series) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = Series(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("a release series is a string or a number, not %s", b)
	}
	*s = Series(n.String())
	return nil
}

// Localized is a text in Portuguese and English.
type Localized struct {
	PT string `json:"pt"`
	EN string `json:"en"`
}

// Template is one of the gallery's: a whole project in one go.
type Template struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Summary   Localized          `json:"summary"`
	Category  string             `json:"category"`
	Languages []string           `json:"languages"`
	Services  []TemplateService  `json:"services"`
	Databases []TemplateDatabase `json:"databases"`
	Buckets   []TemplateBucket   `json:"buckets"`
	Questions []TemplateQuestion `json:"questions"`
	RepoURL   *string            `json:"repo_url"`
	CopyURL   *string            `json:"copy_url"`
	Estimate  *TemplateEstimate  `json:"estimate,omitempty"`
}

type TemplateService struct {
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	Image        *string `json:"image"`
	CronSchedule *string `json:"cron_schedule"`
}

type TemplateDatabase struct {
	Key    string `json:"key"`
	Engine string `json:"engine"`
	Size   string `json:"size"`
}

type TemplateBucket struct {
	Key    string `json:"key"`
	Public bool   `json:"public"`
}

type TemplateQuestion struct {
	Key   string    `json:"key"`
	Label Localized `json:"label"`
	Input string    `json:"input"`
}

type TemplateEstimate struct {
	DatabasesMonthlyMicroCents *string `json:"databases_monthly_micro_cents"`
	AlwaysOnMonthlyMicroCents  *string `json:"always_on_monthly_micro_cents"`
	Sleeps                     bool    `json:"sleeps"`
	SomeSleep                  bool    `json:"some_sleep"`
}

// SourceInspection is what a repository is, and the project that publishes it.
type SourceInspection struct {
	Readable   bool                `json:"readable"`
	Framework  *string             `json:"framework"`
	TemplateID *string             `json:"template_id"`
	Branch     *string             `json:"branch"`
	Services   []InspectedService  `json:"services"`
	Databases  []TemplateDatabase  `json:"databases"`
	Buckets    []TemplateBucket    `json:"buckets"`
	Variables  []InspectedVariable `json:"variables"`
	ToFill     []string            `json:"to_fill"`
	Estimate   *TemplateEstimate   `json:"estimate"`
}

type InspectedService struct {
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	StartCommand   *string `json:"start_command"`
	ReleaseCommand *string `json:"release_command"`
	CronSchedule   *string `json:"cron_schedule"`
}

type InspectedVariable struct {
	Key       string `json:"key"`
	Generated bool   `json:"generated"`
}

// EnvGroup is a project's variable group: variables several services read.
type EnvGroup struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	ServiceIDs    []string `json:"service_ids"`
	VariableCount int      `json:"variable_count"`
}

// RegistryCredential lets image services pull from a private registry.
type RegistryCredential struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	RegistryHost string  `json:"registry_host"`
	Username     string  `json:"username"`
	LastUsedAt   *string `json:"last_used_at"`
	CreatedAt    string  `json:"created_at"`
}

// GitConnection is a GitHub, GitLab or Bitbucket account connected in the console.
type GitConnection struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	AccountLogin string `json:"account_login"`
	Status       string `json:"status"`
}

// TemplateDeployResult is what deploying a template made.
type TemplateDeployResult struct {
	ProjectID string `json:"project_id"`
	Services  []struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		DeploymentID *string `json:"deployment_id"`
	} `json:"services"`
	Databases []string `json:"databases"`
	Buckets   []string `json:"buckets"`
}

// AlertRule is one of a service's alerts: when it fires and whether it does now.
type AlertRule struct {
	// ERROR_RATE, LATENCY_P95, MEMORY or MAX_INSTANCES.
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	// ERROR_RATE: a share (0.05 = 5%); LATENCY_P95: ms; MEMORY: a share of the
	// limit; MAX_INSTANCES: a share of the service's maximum.
	Threshold       float64  `json:"threshold"`
	WindowMinutes   int      `json:"window_minutes"`
	Email           bool     `json:"email"`
	State           string   `json:"state"`
	LastValue       *float64 `json:"last_value"`
	LastEvaluatedAt *string  `json:"last_evaluated_at"`
	Since           *string  `json:"since"`
}

// MetricsSpread is the average instance and the busiest one, a value per step.
type MetricsSpread struct {
	Avg []*float64 `json:"avg"`
	Max []*float64 `json:"max"`
}

// ServiceMetrics is GET /v1/serverless/services/{id}/metrics.
type ServiceMetrics struct {
	Range       string `json:"range"`
	StepSeconds int    `json:"step_seconds"`
	From        string `json:"from"`
	To          string `json:"to"`
	// False when the metrics could not be read just now.
	Available bool   `json:"available"`
	Kind      string `json:"kind"`
	Size      struct {
		VCPU        float64 `json:"vcpu"`
		MemoryBytes float64 `json:"memory_bytes"`
	} `json:"size"`
	Times []int64 `json:"times"`
	// Requests a second, by class; null for workers and cron jobs.
	Requests *struct {
		OK           []*float64 `json:"ok"`
		ClientErrors []*float64 `json:"client_errors"`
		ServerErrors []*float64 `json:"server_errors"`
	} `json:"requests"`
	LatencyMS *struct {
		P50 []*float64 `json:"p50"`
		P95 []*float64 `json:"p95"`
	} `json:"latency_ms"`
	Instances   []*float64    `json:"instances"`
	CPU         MetricsSpread `json:"cpu"`
	MemoryBytes MetricsSpread `json:"memory_bytes"`
	Totals      struct {
		Requests     *float64 `json:"requests"`
		ServerErrors *float64 `json:"server_errors"`
		ErrorRate    *float64 `json:"error_rate"`
		Restarts     int      `json:"restarts"`
		OutOfMemory  int      `json:"out_of_memory"`
		ColdStarts   int      `json:"cold_starts"`
		EgressBytes  *float64 `json:"egress_bytes"`
	} `json:"totals"`
}

// SizeRecommendation is the size the past week says a service should have.
type SizeRecommendation struct {
	// up or down.
	Direction string `json:"direction"`
	// out_of_memory, memory_high, cpu_high or underused.
	Reason string `json:"reason"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Instances out of memory, or the share of CPU / memory used (0–1).
	Measure float64 `json:"measure"`
	// The change a month, µ¢ (negative: a saving).
	MonthlyDeltaMicroCents *string `json:"monthly_delta_micro_cents"`
	Days                   int     `json:"days"`
}

// ServiceRecommendation is GET /v1/serverless/services/{id}/recommendation.
type ServiceRecommendation struct {
	Recommendation *SizeRecommendation `json:"recommendation"`
}

// AccessLogLine is one request a web or private service answered.
type AccessLogLine struct {
	TS        string  `json:"ts"`
	Instance  string  `json:"instance"`
	Revision  *int    `json:"revision"`
	Method    string  `json:"method"`
	Host      string  `json:"host"`
	Path      string  `json:"path"`
	Status    int     `json:"status"`
	Bytes     int64   `json:"bytes"`
	LatencyMS float64 `json:"latency_ms"`
	IP        *string `json:"ip"`
	UserAgent *string `json:"user_agent"`
}

// AccessLogs is GET /v1/serverless/services/{id}/access-logs.
type AccessLogs struct {
	Lines     []AccessLogLine `json:"lines"`
	Truncated bool            `json:"truncated"`
}

// Egress is GET /v1/serverless/egress: what services sent to visitors this
// period, of what the period includes (decimal GB, 10^9 bytes).
type Egress struct {
	PeriodFrom           string  `json:"period_from"`
	PeriodTo             string  `json:"period_to"`
	UsedBytes            string  `json:"used_bytes"`
	AllowanceBytes       string  `json:"allowance_bytes"`
	PriceMicroCentsPerGB *string `json:"price_micro_cents_per_gb"`
	ChargedMicroCents    string  `json:"charged_micro_cents"`
	Services             []struct {
		ServiceID string  `json:"service_id"`
		Name      *string `json:"name"`
		Bytes     string  `json:"bytes"`
	} `json:"services"`
}

// Webhook is one of the account's webhooks. Its address is shown cut (it is a
// credential); a signing secret only ever comes in the answer that makes one.
type Webhook struct {
	ID string `json:"id"`
	// GENERIC (signed JSON to your address), SLACK or DISCORD.
	Kind        string  `json:"kind"`
	URL         string  `json:"url"`
	Description *string `json:"description"`
	// Empty: every event.
	Events              []string `json:"events"`
	Enabled             bool     `json:"enabled"`
	DisabledReason      *string  `json:"disabled_reason"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	LastSuccessAt       *string  `json:"last_success_at"`
	LastFailureAt       *string  `json:"last_failure_at"`
	CreatedAt           string   `json:"created_at"`
}

// CreatedWebhook is a new webhook and, for GENERIC, its signing secret.
type CreatedWebhook struct {
	Webhook Webhook `json:"webhook"`
	Secret  *string `json:"secret"`
}

// WebhookTest is what sending a webhook.test event now got back.
type WebhookTest struct {
	OK     bool    `json:"ok"`
	Status *int    `json:"status"`
	Error  *string `json:"error"`
}

// WebhookDelivery is one event sent (or being retried) to a webhook.
type WebhookDelivery struct {
	ID             string  `json:"id"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	Status         string  `json:"status"`
	Attempts       int     `json:"attempts"`
	LastStatusCode *int    `json:"last_status_code"`
	LastError      *string `json:"last_error"`
	NextAttemptAt  *string `json:"next_attempt_at"`
	DeliveredAt    *string `json:"delivered_at"`
	CreatedAt      string  `json:"created_at"`
}
