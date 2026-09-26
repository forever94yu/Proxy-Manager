import { apiRequest, queryString } from '@/api/client'
import type {
  BulkServerActionInput,
  BulkServiceActionInput,
  DashboardData,
  Job,
  JobMutationResult,
  ListResponse,
  Operator,
  ProxyUser,
  ProxyUserInput,
  Server,
  ServerInput,
  ServerMutationResult,
  UserMutationResult,
} from '@/types'

type AuthResponse = Operator | { user: Operator }
type ServerResponse = Server | ServerMutationResult
type UserResponse = ProxyUser | UserMutationResult
type ListPayload<T> = T[] | ListResponse<T>

function normalizeOperator(payload: AuthResponse): Operator {
  return 'user' in payload ? payload.user : payload
}

function normalizeList<T>(payload: ListPayload<T>): T[] {
  return Array.isArray(payload) ? payload : payload.items
}

function normalizeServerMutation(payload: ServerResponse | undefined): ServerMutationResult {
  if (!payload) return {}
  return 'id' in payload ? { server: payload } : payload
}

function normalizeUserMutation(payload: UserResponse | undefined): UserMutationResult {
  if (!payload) return {}
  return 'id' in payload ? { user: payload } : payload
}

export const authApi = {
  async login(username: string, password: string): Promise<Operator> {
    return normalizeOperator(await apiRequest<AuthResponse>('/auth/login', {
      method: 'POST',
      body: { username, password },
    }))
  },
  async logout(): Promise<void> {
    await apiRequest('/auth/logout', { method: 'POST' })
  },
  async me(): Promise<Operator> {
    return normalizeOperator(await apiRequest<AuthResponse>('/auth/me'))
  },
}

export const dashboardApi = {
  get(): Promise<DashboardData> {
    return apiRequest('/dashboard')
  },
}

export const serversApi = {
  async list(filters: { search?: string; status?: string } = {}): Promise<Server[]> {
    const payload = await apiRequest<ListPayload<Server>>(`/servers${queryString(filters)}`)
    return normalizeList(payload)
  },
  async create(input: ServerInput): Promise<ServerMutationResult> {
    return normalizeServerMutation(await apiRequest<ServerResponse>('/servers', { method: 'POST', body: input }))
  },
  async update(id: string, input: ServerInput): Promise<ServerMutationResult> {
    return normalizeServerMutation(await apiRequest<ServerResponse>(`/servers/${encodeURIComponent(id)}`, { method: 'PUT', body: input }))
  },
  async remove(id: string): Promise<ServerMutationResult> {
    return normalizeServerMutation(await apiRequest<ServerMutationResult | undefined>(`/servers/${encodeURIComponent(id)}`, { method: 'DELETE' }))
  },
  test(id: string): Promise<JobMutationResult> {
    return apiRequest(`/servers/${encodeURIComponent(id)}/test`, { method: 'POST', body: {} })
  },
  deploy(id: string): Promise<JobMutationResult> {
    return apiRequest(`/servers/${encodeURIComponent(id)}/deploy`, { method: 'POST', body: {} })
  },
  service(id: string, action: 'start' | 'stop' | 'restart'): Promise<JobMutationResult> {
    return apiRequest(`/servers/${encodeURIComponent(id)}/service`, { method: 'POST', body: { action } })
  },
  deployMany(input: BulkServerActionInput): Promise<JobMutationResult> {
    return apiRequest('/servers/actions/deploy', { method: 'POST', body: input })
  },
  serviceMany(input: BulkServiceActionInput): Promise<JobMutationResult> {
    return apiRequest('/servers/actions/service', { method: 'POST', body: input })
  },
}

export const usersApi = {
  async list(filters: { search?: string; syncStatus?: string; status?: string } = {}): Promise<ProxyUser[]> {
    const payload = await apiRequest<ListPayload<ProxyUser>>(`/users${queryString(filters)}`)
    return normalizeList(payload)
  },
  async create(input: ProxyUserInput): Promise<UserMutationResult> {
    return normalizeUserMutation(await apiRequest<UserResponse>('/users', { method: 'POST', body: input }))
  },
  async update(id: string, input: ProxyUserInput): Promise<UserMutationResult> {
    return normalizeUserMutation(await apiRequest<UserResponse>(`/users/${encodeURIComponent(id)}`, { method: 'PUT', body: input }))
  },
  async remove(id: string): Promise<UserMutationResult> {
    return normalizeUserMutation(await apiRequest<UserMutationResult | undefined>(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' }))
  },
  /** Clears current-period usage; `job` is absent when the user has no servers. */
  async resetTraffic(id: string): Promise<UserMutationResult> {
    return normalizeUserMutation(await apiRequest<UserResponse | undefined>(`/users/${encodeURIComponent(id)}/traffic/reset`, { method: 'POST', body: {} }))
  },
  async setEnabled(id: string, enabled: boolean): Promise<UserMutationResult> {
    return normalizeUserMutation(await apiRequest<UserResponse | undefined>(`/users/${encodeURIComponent(id)}/state`, { method: 'POST', body: { enabled } }))
  },
}

export const jobsApi = {
  async list(filters: { status?: string; type?: string } = {}): Promise<Job[]> {
    const payload = await apiRequest<ListPayload<Job>>(`/jobs${queryString(filters)}`)
    return normalizeList(payload)
  },
  get(id: string): Promise<Job> {
    return apiRequest(`/jobs/${encodeURIComponent(id)}`)
  },
  retry(id: string): Promise<JobMutationResult> {
    return apiRequest(`/jobs/${encodeURIComponent(id)}/retry`, { method: 'POST', body: {} })
  },
}
