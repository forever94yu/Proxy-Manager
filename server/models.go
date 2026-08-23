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
	CreatedAt        time.Time  `json:"-"`
	UpdatedAt        time.Time  `json:"-"`
}

type ProxyUser struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	PasswordCipher string    `json:"-"`
	ServerIDs      []string  `json:"serverIds"`
	ServerCount    int       `json:"serverCount"`
	SyncStatus     string    `json:"syncStatus"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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
