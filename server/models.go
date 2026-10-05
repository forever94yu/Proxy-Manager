package main

import "time"

type Operator struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Server struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Host             string     `json:"host"`
	SSHPort          int        `json:"sshPort"`
	SSHUser          string     `json:"sshUser"`
	AuthMethod       string     `json:"authMethod"`
	CredentialCipher string     `json:"-"`
	HostFingerprint  string     `json:"-"`
	ConfigRevision   int        `json:"-"`
	Status           string     `json:"status"`
	ServiceStatus    string     `json:"serviceStatus"`
	InstallStatus    string     `json:"installStatus"`
	OS               string     `json:"os,omitempty"`
	Version          string     `json:"version,omitempty"`
	PublicIP         string     `json:"-"`
	HTTPPort         int        `json:"httpPort,omitempty"`
	SocksPort        int        `json:"socksPort,omitempty"`
	DNS              []string   `json:"dns"`
	UserCount        int        `json:"userCount"`
	DesiredUserCount int        `json:"-"`
	LastSeenAt       *time.Time `json:"lastSeenAt,omitempty"`
	Tags             []string   `json:"tags"`
	TrafficSyncedAt  *time.Time `json:"trafficSyncedAt,omitempty"`
	TrafficError     string     `json:"trafficError,omitempty"`
	CreatedAt        time.Time  `json:"-"`
	UpdatedAt        time.Time  `json:"-"`
}

type ProxyUser struct {
	ID             string   `json:"id"`
	Username       string   `json:"username"`
	PasswordCipher string   `json:"-"`
	ServerIDs      []string `json:"serverIds"`
	ServerCount    int      `json:"serverCount"`
	SyncStatus     string   `json:"syncStatus"`

	// Usage limits. Status is derived: disabled > expired > exhausted > active.
	Enabled           bool       `json:"enabled"`
	Status            string     `json:"status"`
	TrafficLimitBytes int64      `json:"trafficLimitBytes"`
	TrafficUsedBytes  int64      `json:"trafficUsedBytes"`
	TrafficUpdatedAt  *time.Time `json:"trafficUpdatedAt,omitempty"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	ResetPeriod       string     `json:"resetPeriod"`
	ResetAnchor       string     `json:"resetAnchor,omitempty"`
	PeriodToken       int64      `json:"-"`
	PeriodStartedAt   *time.Time `json:"periodStartedAt,omitempty"`
	NextResetAt       *time.Time `json:"nextResetAt,omitempty"`
	LastResetAt       *time.Time `json:"lastResetAt,omitempty"`

	// SubscriptionVersion is signed into the subscription URL; incrementing
	// it revokes the URL.
	SubscriptionVersion int64 `json:"-"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Job struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Status       string      `json:"status"`
	Progress     int         `json:"progress"`
	TargetCount  int         `json:"targetCount"`
	SuccessCount int         `json:"successCount"`
	FailedCount  int         `json:"failedCount"`
	Actor        string      `json:"actor"`
	EntityType   string      `json:"-"`
	EntityID     string      `json:"-"`
	Message      string      `json:"message,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
	StartedAt    *time.Time  `json:"startedAt,omitempty"`
	FinishedAt   *time.Time  `json:"finishedAt,omitempty"`
	Targets      []JobTarget `json:"targets,omitempty"`
}

type JobTarget struct {
	ID         string     `json:"-"`
	JobID      string     `json:"-"`
	ServerID   string     `json:"serverId"`
	ServerName string     `json:"serverName"`
	Status     string     `json:"status"`
	Payload    string     `json:"-"`
	Attempt    int        `json:"attempt"`
	Error      string     `json:"error"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type ServerInput struct {
	Name       string   `json:"name"`
	Host       string   `json:"host"`
	SSHPort    int      `json:"sshPort"`
	SSHUser    string   `json:"sshUser"`
	AuthMethod string   `json:"authMethod"`
	Credential string   `json:"credential"`
	HTTPPort   int      `json:"httpPort"`
	SocksPort  int      `json:"socksPort"`
	DNS        []string `json:"dns"`
	Tags       []string `json:"tags"`
}

type UserInput struct {
	Username     string   `json:"username"`
	PasswordMode string   `json:"passwordMode"`
	Password     string   `json:"password"`
	ServerIDs    []string `json:"serverIds"`

	// Usage limits are optional. Omitted fields keep their current value when
	// editing and use the defaults (enabled, unlimited, permanent, no reset)
	// when creating.
	Enabled           *bool   `json:"enabled"`
	TrafficLimitBytes *int64  `json:"trafficLimitBytes"`
	ExpiresAt         *string `json:"expiresAt"`
	ResetPeriod       *string `json:"resetPeriod"`
	ResetAnchor       *string `json:"resetAnchor"`
}

// NodePolicy is the traffic policy of one proxy user on one node: whether the
// account may authenticate, its node-local total traffic cap and the
// accounting period whose counters the node must use.
type NodePolicy struct {
	Username string `json:"username"`
	State    string `json:"state"`
	CapMB    int64  `json:"capMb"`
	Period   int64  `json:"period"`
}

type TargetTask struct {
	Action             string   `json:"action"`
	Username           string   `json:"username,omitempty"`
	OldUsername        string   `json:"oldUsername,omitempty"`
	PasswordCiphertext string   `json:"passwordCiphertext,omitempty"`
	PublicIP           string   `json:"publicIp,omitempty"`
	HTTPPort           int      `json:"httpPort,omitempty"`
	SocksPort          int      `json:"socksPort,omitempty"`
	DNS                []string `json:"dns,omitempty"`
	ServiceAction      string   `json:"serviceAction,omitempty"`

	// Policies are resolved by the worker immediately before execution, so a
	// queued or retried job always applies the latest desired state. They are
	// never persisted in job payloads.
	Policy   *NodePolicy  `json:"-"`
	Policies []NodePolicy `json:"-"`
}

// TrafficReport is a snapshot of the proxy users and traffic policies present
// on a node. The installer prints one after every account or policy change and
// for `--api traffic`.
type TrafficReport struct {
	ObservedAt time.Time
	NodeUsers  []string
	Entries    []TrafficEntry
}

type TrafficEntry struct {
	Username string
	// State is enabled, disabled, or removed (final counter value of a policy
	// entry that was deleted together with the account).
	State  string
	CapMB  int64
	Period int64
	Index  int
	Bytes  int64
}

type Dashboard struct {
	Stats      DashboardStats `json:"stats"`
	Servers    []Server       `json:"servers"`
	RecentJobs []Job          `json:"recentJobs"`
}

type DashboardStats struct {
	TotalServers    int `json:"totalServers"`
	OnlineServers   int `json:"onlineServers"`
	RunningServices int `json:"runningServices"`
	TotalUsers      int `json:"totalUsers"`
	FailedJobs      int `json:"failedJobs"`
}
