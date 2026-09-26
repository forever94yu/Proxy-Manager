export type OperatorRole = 'admin' | 'operator' | 'viewer'

export interface Operator {
  id: string
  name: string
  username: string
  role: OperatorRole
}

export type ConnectivityStatus = 'online' | 'offline' | 'unknown'
export type ServiceStatus = 'running' | 'stopped' | 'failed' | 'unknown'
export type InstallStatus = 'installed' | 'deploying' | 'not_installed' | 'failed'

export interface Server {
  id: string
  name: string
  host: string
  sshPort: number
  sshUser: string
  authMethod?: AuthMethod
  status: ConnectivityStatus
  serviceStatus: ServiceStatus
  installStatus: InstallStatus
  os?: string
  version?: string
  httpPort?: number
  socksPort?: number
  dns?: string[]
  userCount?: number
  lastSeenAt?: string
  tags?: string[]
  /** Last successful traffic collection. */
  trafficSyncedAt?: string
  /** Error of the last traffic collection; absent when it succeeded. */
  trafficError?: string
}

export type AuthMethod = 'key' | 'password'

export interface ServerInput {
  name: string
  host: string
  sshPort: number
  sshUser: string
  authMethod: AuthMethod
  credential?: string
  httpPort: number
  socksPort: number
  dns: string[]
  tags: string[]
}

export interface BulkServerActionInput {
  serverIds: string[]
}

export interface BulkServiceActionInput extends BulkServerActionInput {
  action: 'start' | 'stop' | 'restart'
}

export type UserSyncStatus = 'synced' | 'pending' | 'partial' | 'failed'
export type PasswordMode = 'generated' | 'custom' | 'unchanged'
/** Derived by the server: disabled (manual) > expired > exhausted > active. */
export type ProxyUserStatus = 'active' | 'disabled' | 'expired' | 'exhausted'
export type ResetPeriod = 'none' | 'daily' | 'weekly' | 'monthly'
export type PeriodicResetPeriod = Exclude<ResetPeriod, 'none'>

export interface ProxyUser {
  id: string
  username: string
  serverIds?: string[]
  serverCount?: number
  syncStatus: UserSyncStatus
  enabled: boolean
  status: ProxyUserStatus
  /** 0 = unlimited. */
  trafficLimitBytes: number
  /** Usage of the current period summed across servers. */
  trafficUsedBytes: number
  trafficUpdatedAt?: string
  /** Absent = never expires. */
  expiresAt?: string
  resetPeriod: ResetPeriod
  /** Present when resetPeriod is not 'none'. */
  resetAnchor?: string
  periodStartedAt?: string
  nextResetAt?: string
  lastResetAt?: string
  createdAt?: string
  updatedAt?: string
}

export interface ProxyUserInput {
  username: string
  passwordMode: PasswordMode
  password?: string
  serverIds: string[]
  enabled: boolean
  /** Integer bytes, 0 = unlimited, max 1 PiB. */
  trafficLimitBytes: number
  /** '' = never expires; otherwise RFC 3339 with the local UTC offset. */
  expiresAt: string
  resetPeriod: ResetPeriod
  /** '' when resetPeriod is 'none'; otherwise RFC 3339 with the local UTC offset. */
  resetAnchor: string
}

export type JobStatus =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'partially_failed'
  | 'failed'
  | 'cancelled'

export type JobType =
  | 'deploy'
  | 'user_create'
  | 'user_update'
  | 'user_delete'
  | 'user_policy'
  | 'user_traffic_reset'
  | 'user_enable'
  | 'user_disable'
  | 'service_start'
  | 'service_stop'
  | 'service_restart'
  | 'connection_test'
  | string

export type JobTargetStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface JobTarget {
  serverId: string
  serverName?: string
  status: JobTargetStatus
  attempt?: number
  error?: string
  startedAt?: string
  finishedAt?: string
}

export interface Job {
  id: string
  type: JobType
  status: JobStatus
  progress?: number
  targetCount?: number
  successCount?: number
  failedCount?: number
  actor?: string
  createdAt: string
  startedAt?: string
  finishedAt?: string
  message?: string
  targets?: JobTarget[]
}

export interface DashboardStats {
  totalServers: number
  onlineServers: number
  runningServices: number
  totalUsers: number
  failedJobs: number
}

export interface DashboardData {
  stats: DashboardStats
  servers: Server[]
  recentJobs: Job[]
}

export interface ServerMutationResult {
  server?: Server
  job?: Job
  message?: string
}

export interface UserMutationResult {
  user?: ProxyUser
  job?: Job
  generatedPassword?: string
}

export interface JobMutationResult {
  job?: Job
  message?: string
}

export interface ApiErrorPayload {
  error?: {
    code?: string
    message?: string
    fields?: Record<string, string>
  }
  message?: string
}

export interface ListResponse<T> {
  items: T[]
  total?: number
}
