import type {
  DashboardData,
  Job,
  JobMutationResult,
  Operator,
  ProxyUser,
  ProxyUserInput,
  Server,
  ServerInput,
  UserMutationResult,
} from '@/types'

const now = Date.now()
const minutesAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString()

const operator: Operator = {
  id: 'op-demo',
  name: '系统管理员',
  username: 'admin',
  role: 'admin',
}

let servers: Server[] = [
  {
    id: 'srv-sh-01',
    name: '上海出口 01',
    host: '10.24.8.11',
    sshPort: 22,
    sshUser: 'proxymgr',
    authMethod: 'key',
    status: 'online',
    serviceStatus: 'running',
    installStatus: 'installed',
    os: 'Ubuntu 24.04',
    version: '0.9.4',
    httpPort: 3128,
    socksPort: 1080,
    dns: ['1.1.1.1', '1.0.0.1'],
    userCount: 18,
    lastSeenAt: minutesAgo(1),
    tags: ['生产', '华东'],
  },
  {
    id: 'srv-sh-02',
    name: '上海出口 02',
    host: '10.24.8.12',
    sshPort: 22,
    sshUser: 'proxymgr',
    authMethod: 'key',
    status: 'online',
    serviceStatus: 'running',
    installStatus: 'installed',
    os: 'Debian 12',
    version: '0.9.4',
    httpPort: 3128,
    socksPort: 1080,
    dns: ['1.1.1.1', '1.0.0.1'],
    userCount: 16,
    lastSeenAt: minutesAgo(2),
    tags: ['生产', '华东'],
  },
  {
    id: 'srv-gz-01',
    name: '广州出口 01',
    host: '10.32.6.21',
    sshPort: 2202,
    sshUser: 'proxymgr',
    authMethod: 'key',
    status: 'online',
    serviceStatus: 'stopped',
    installStatus: 'installed',
    os: 'Ubuntu 22.04',
    version: '0.9.3',
    httpPort: 3128,
    socksPort: 1080,
    dns: ['8.8.8.8', '8.8.4.4'],
    userCount: 12,
    lastSeenAt: minutesAgo(4),
    tags: ['生产', '华南'],
  },
  {
    id: 'srv-hk-01',
    name: '香港边缘 01',
    host: '172.18.4.35',
    sshPort: 22,
    sshUser: 'ubuntu',
    authMethod: 'key',
    status: 'offline',
    serviceStatus: 'unknown',
    installStatus: 'installed',
    os: 'Ubuntu 24.04',
    version: '0.9.4',
    httpPort: 8080,
    socksPort: 1080,
    dns: ['1.1.1.1', '1.0.0.1'],
    userCount: 9,
    lastSeenAt: minutesAgo(38),
    tags: ['生产', '境外'],
  },
  {
    id: 'srv-test-01',
    name: '预发布验证机',
    host: '10.40.2.9',
    sshPort: 22,
    sshUser: 'root',
    authMethod: 'password',
    status: 'online',
    serviceStatus: 'failed',
    installStatus: 'failed',
    os: 'CentOS Stream 10',
    httpPort: 3128,
    socksPort: 1080,
    dns: ['9.9.9.9'],
    userCount: 0,
    lastSeenAt: minutesAgo(3),
    tags: ['预发布'],
  },
  {
    id: 'srv-new-01',
    name: '北京出口 01',
    host: '10.18.3.15',
    sshPort: 22,
    sshUser: 'proxymgr',
    authMethod: 'key',
    status: 'unknown',
    serviceStatus: 'unknown',
    installStatus: 'not_installed',
    os: 'Debian 13',
    userCount: 0,
    dns: ['1.1.1.1', '1.0.0.1'],
    tags: ['待部署', '华北'],
  },
]

let users: ProxyUser[] = [
  { id: 'usr-001', username: 'crawler_cn', serverIds: ['srv-sh-01', 'srv-sh-02', 'srv-gz-01'], serverCount: 3, syncStatus: 'synced', createdAt: minutesAgo(43_200), updatedAt: minutesAgo(80) },
  { id: 'usr-002', username: 'monitoring', serverIds: ['srv-sh-01', 'srv-sh-02', 'srv-gz-01', 'srv-hk-01'], serverCount: 4, syncStatus: 'partial', createdAt: minutesAgo(36_000), updatedAt: minutesAgo(38) },
  { id: 'usr-003', username: 'qa_runner', serverIds: ['srv-test-01'], serverCount: 1, syncStatus: 'failed', createdAt: minutesAgo(12_000), updatedAt: minutesAgo(26) },
  { id: 'usr-004', username: 'data_pipeline', serverIds: ['srv-sh-01', 'srv-sh-02'], serverCount: 2, syncStatus: 'synced', createdAt: minutesAgo(72_000), updatedAt: minutesAgo(1_440) },
  { id: 'usr-005', username: 'vendor_api', serverIds: ['srv-gz-01'], serverCount: 1, syncStatus: 'pending', createdAt: minutesAgo(50), updatedAt: minutesAgo(8) },
]

let jobs: Job[] = [
  {
    id: 'job-a18f42c1', type: 'user_create', status: 'running', progress: 68, targetCount: 3, successCount: 2, failedCount: 0, actor: '系统管理员', createdAt: minutesAgo(8), startedAt: minutesAgo(8), message: '正在同步到广州出口 01',
    targets: [
      { serverId: 'srv-sh-01', serverName: '上海出口 01', status: 'succeeded', attempt: 1, startedAt: minutesAgo(8), finishedAt: minutesAgo(7) },
      { serverId: 'srv-sh-02', serverName: '上海出口 02', status: 'succeeded', attempt: 1, startedAt: minutesAgo(8), finishedAt: minutesAgo(7) },
      { serverId: 'srv-gz-01', serverName: '广州出口 01', status: 'running', attempt: 1, startedAt: minutesAgo(7) },
    ],
  },
  {
    id: 'job-c0319b8e', type: 'connection_test', status: 'failed', progress: 100, targetCount: 1, successCount: 0, failedCount: 1, actor: '系统管理员', createdAt: minutesAgo(26), startedAt: minutesAgo(26), finishedAt: minutesAgo(25), message: 'SSH 握手超时',
    targets: [{ serverId: 'srv-hk-01', serverName: '香港边缘 01', status: 'failed', attempt: 3, error: 'SSH handshake timeout after 15s; host 172.18.4.35 did not respond', startedAt: minutesAgo(26), finishedAt: minutesAgo(25) }],
  },
  {
    id: 'job-1fe08aa7', type: 'service_restart', status: 'succeeded', progress: 100, targetCount: 1, successCount: 1, failedCount: 0, actor: '运维值班', createdAt: minutesAgo(73), startedAt: minutesAgo(72), finishedAt: minutesAgo(71), message: '服务健康检查通过',
    targets: [{ serverId: 'srv-sh-01', serverName: '上海出口 01', status: 'succeeded', attempt: 1, startedAt: minutesAgo(72), finishedAt: minutesAgo(71) }],
  },
  {
    id: 'job-7b02ed13', type: 'deploy', status: 'partially_failed', progress: 100, targetCount: 4, successCount: 3, failedCount: 1, actor: '系统管理员', createdAt: minutesAgo(180), startedAt: minutesAgo(179), finishedAt: minutesAgo(166), message: '1 个节点部署失败',
    targets: [
      { serverId: 'srv-sh-01', serverName: '上海出口 01', status: 'succeeded', attempt: 1, startedAt: minutesAgo(179), finishedAt: minutesAgo(171) },
      { serverId: 'srv-sh-02', serverName: '上海出口 02', status: 'succeeded', attempt: 1, startedAt: minutesAgo(179), finishedAt: minutesAgo(170) },
      { serverId: 'srv-gz-01', serverName: '广州出口 01', status: 'succeeded', attempt: 1, startedAt: minutesAgo(178), finishedAt: minutesAgo(169) },
      { serverId: 'srv-hk-01', serverName: '香港边缘 01', status: 'failed', attempt: 3, error: '远端安装步骤超时，未能在 300 秒内完成服务健康检查。', startedAt: minutesAgo(177), finishedAt: minutesAgo(166) },
    ],
  },
  { id: 'job-9d334a21', type: 'user_update', status: 'succeeded', progress: 100, targetCount: 2, successCount: 2, failedCount: 0, actor: '系统管理员', createdAt: minutesAgo(360), startedAt: minutesAgo(359), finishedAt: minutesAgo(358), message: '密码轮换完成' },
  { id: 'job-83e4cbd0', type: 'deploy', status: 'queued', progress: 0, targetCount: 1, successCount: 0, failedCount: 0, actor: '系统管理员', createdAt: minutesAgo(2), message: '等待可用执行器' },
]

function clone<T>(value: T): T {
  return structuredClone(value)
}

function uniqueId(prefix: string): string {
  return `${prefix}-${Math.random().toString(16).slice(2, 10)}`
}

function randomPassword(): string {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
  return Array.from({ length: 16 }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join('')
}

function createJob(type: string, targetCount = 1, message = '任务已进入执行队列'): Job {
  const job: Job = {
    id: uniqueId('job'),
    type,
    status: 'queued',
    progress: 0,
    targetCount,
    successCount: 0,
    failedCount: 0,
    actor: operator.name,
    createdAt: new Date().toISOString(),
    message,
  }
  jobs = [job, ...jobs]
  return job
}

function isAuthenticated(): boolean {
  return sessionStorage.getItem('pm_demo_auth') === '1'
}

async function latency(): Promise<void> {
  await new Promise((resolve) => window.setTimeout(resolve, 180 + Math.random() * 260))
}

export class DemoApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export async function demoRequest<T>(path: string, method: string, body?: unknown): Promise<T> {
  await latency()
  const url = new URL(path, 'https://demo.proxy-manager.local')
  const pathname = url.pathname.replace(/^\/api\/v1/, '')

  if (pathname === '/auth/login' && method === 'POST') {
    const credentials = body as { username?: string; password?: string }
    if (!credentials.username || !credentials.password) throw new DemoApiError(422, '请输入账号和密码')
    sessionStorage.setItem('pm_demo_auth', '1')
    return clone({ user: operator }) as T
  }

  if (pathname === '/auth/logout' && method === 'POST') {
    sessionStorage.removeItem('pm_demo_auth')
    return {} as T
  }

  if (pathname === '/auth/me' && method === 'GET') {
    if (!isAuthenticated()) throw new DemoApiError(401, '登录状态已失效')
    return clone({ user: operator }) as T
  }

  if (!isAuthenticated()) throw new DemoApiError(401, '请先登录')

  if (pathname === '/dashboard' && method === 'GET') {
    const dashboard: DashboardData = {
      stats: {
        totalServers: servers.length,
        onlineServers: servers.filter((server) => server.status === 'online').length,
        runningServices: servers.filter((server) => server.serviceStatus === 'running').length,
        totalUsers: users.length,
        failedJobs: jobs.filter((job) => job.status === 'failed' || job.status === 'partially_failed').length,
      },
      servers: servers.slice(0, 5),
      recentJobs: jobs.slice(0, 5),
    }
    return clone(dashboard) as T
  }

  if (pathname === '/servers' && method === 'GET') {
    const search = url.searchParams.get('search')?.toLowerCase() || ''
    const status = url.searchParams.get('status') || ''
    const items = servers.filter((server) => {
      const matchesSearch = !search || `${server.name} ${server.host} ${server.tags?.join(' ')}`.toLowerCase().includes(search)
      const matchesStatus = !status || server.status === status || server.serviceStatus === status || server.installStatus === status
      return matchesSearch && matchesStatus
    })
    return clone({ items, total: items.length }) as T
  }

  if (pathname === '/servers' && method === 'POST') {
    const input = body as ServerInput
    const server: Server = {
      id: uniqueId('srv'),
      name: input.name,
      host: input.host,
      sshPort: input.sshPort,
      sshUser: input.sshUser,
      authMethod: input.authMethod,
      status: 'unknown',
      serviceStatus: 'unknown',
      installStatus: 'not_installed',
      httpPort: input.httpPort,
      socksPort: input.socksPort,
      dns: input.dns,
      userCount: 0,
      tags: input.tags,
    }
    servers = [server, ...servers]
    return clone({ server }) as T
  }

  if (pathname === '/servers/actions/deploy' && method === 'POST') {
    const serverIds = (body as { serverIds?: string[] }).serverIds || []
    const targets = servers.filter((server) => serverIds.includes(server.id))
    if (!targets.length) throw new DemoApiError(422, '请选择至少一台服务器')
    targets.forEach((server) => { server.installStatus = 'deploying' })
    const job = createJob('deploy', targets.length, `正在批量部署 ${targets.length} 台服务器`)
    return clone({ job } satisfies JobMutationResult) as T
  }

  if (pathname === '/servers/actions/service' && method === 'POST') {
    const input = body as { serverIds?: string[]; action?: 'start' | 'stop' | 'restart' }
    const targets = servers.filter((server) => input.serverIds?.includes(server.id))
    if (!targets.length) throw new DemoApiError(422, '请选择至少一台服务器')
    const action = input.action || 'restart'
    if (action === 'start') targets.forEach((server) => { server.serviceStatus = 'running' })
    if (action === 'stop') targets.forEach((server) => { server.serviceStatus = 'stopped' })
    const label = action === 'start' ? '启动' : action === 'stop' ? '停止' : '重启'
    const job = createJob(`service_${action}`, targets.length, `正在批量${label} ${targets.length} 台服务器`)
    return clone({ job } satisfies JobMutationResult) as T
  }

  const serverMatch = pathname.match(/^\/servers\/([^/]+)$/)
  if (serverMatch) {
    const id = decodeURIComponent(serverMatch[1])
    const index = servers.findIndex((server) => server.id === id)
    if (index < 0) throw new DemoApiError(404, '服务器不存在')

    if (method === 'PUT') {
      const input = body as ServerInput
      servers[index] = {
        ...servers[index],
        name: input.name,
        host: input.host,
        sshPort: input.sshPort,
        sshUser: input.sshUser,
        authMethod: input.authMethod,
        httpPort: input.httpPort,
        socksPort: input.socksPort,
        dns: input.dns,
        tags: input.tags,
      }
      return clone({ server: servers[index] }) as T
    }

    if (method === 'DELETE') {
      servers = servers.filter((server) => server.id !== id)
      users = users.map((user) => ({
        ...user,
        serverIds: user.serverIds?.filter((serverId) => serverId !== id),
        serverCount: user.serverIds?.filter((serverId) => serverId !== id).length,
      }))
      return {} as T
    }
  }

  const serverActionMatch = pathname.match(/^\/servers\/([^/]+)\/(test|deploy|service)$/)
  if (serverActionMatch && method === 'POST') {
    const id = decodeURIComponent(serverActionMatch[1])
    const action = serverActionMatch[2]
    const server = servers.find((item) => item.id === id)
    if (!server) throw new DemoApiError(404, '服务器不存在')

    if (action === 'test') {
      const job = createJob('connection_test', 1, `正在连接 ${server.name}`)
      return clone({ job, message: '连接测试已开始' } satisfies JobMutationResult) as T
    }

    if (action === 'deploy') {
      server.installStatus = 'deploying'
      const job = createJob('deploy', 1, `正在部署 ${server.name}`)
      return clone({ job } satisfies JobMutationResult) as T
    }

    const serviceAction = (body as { action?: string }).action || 'restart'
    const job = createJob(`service_${serviceAction}`, 1, `正在${serviceAction === 'start' ? '启动' : serviceAction === 'stop' ? '停止' : '重启'} ${server.name}`)
    if (serviceAction === 'start') server.serviceStatus = 'running'
    if (serviceAction === 'stop') server.serviceStatus = 'stopped'
    return clone({ job } satisfies JobMutationResult) as T
  }

  if (pathname === '/users' && method === 'GET') {
    const search = url.searchParams.get('search')?.toLowerCase() || ''
    const syncStatus = url.searchParams.get('syncStatus') || ''
    const items = users.filter((user) => {
      const matchesSearch = !search || user.username.toLowerCase().includes(search)
      const matchesStatus = !syncStatus || user.syncStatus === syncStatus
      return matchesSearch && matchesStatus
    })
    return clone({ items, total: items.length }) as T
  }

  if (pathname === '/users' && method === 'POST') {
    const input = body as ProxyUserInput
    if (users.some((user) => user.username === input.username)) throw new DemoApiError(409, '代理用户名已存在')
    const user: ProxyUser = {
      id: uniqueId('usr'),
      username: input.username,
      serverIds: input.serverIds,
      serverCount: input.serverIds.length,
      syncStatus: 'pending',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }
    users = [user, ...users]
    const job = createJob('user_create', input.serverIds.length, `正在分发账号 ${input.username}`)
    const result: UserMutationResult = {
      user,
      job,
      generatedPassword: input.passwordMode === 'generated' ? randomPassword() : undefined,
    }
    return clone(result) as T
  }

  const userMatch = pathname.match(/^\/users\/([^/]+)$/)
  if (userMatch) {
    const id = decodeURIComponent(userMatch[1])
    const index = users.findIndex((user) => user.id === id)
    if (index < 0) throw new DemoApiError(404, '代理用户不存在')

    if (method === 'PUT') {
      const input = body as ProxyUserInput
      users[index] = {
        ...users[index],
        username: input.username,
        serverIds: input.serverIds,
        serverCount: input.serverIds.length,
        syncStatus: 'pending',
        updatedAt: new Date().toISOString(),
      }
      const job = createJob('user_update', input.serverIds.length, `正在更新账号 ${input.username}`)
      const result: UserMutationResult = {
        user: users[index],
        job,
        generatedPassword: input.passwordMode === 'generated' ? randomPassword() : undefined,
      }
      return clone(result) as T
    }

    if (method === 'DELETE') {
      const user = users[index]
      users = users.filter((item) => item.id !== id)
      const result: UserMutationResult = {
        job: createJob('user_delete', user.serverCount || 0, `正在从目标服务器移除 ${user.username}`),
      }
      return clone(result) as T
    }
  }

  if (pathname === '/jobs' && method === 'GET') {
    const status = url.searchParams.get('status') || ''
    const type = url.searchParams.get('type') || ''
    const items = jobs.filter((job) => (!status || job.status === status) && (!type || job.type === type))
    return clone({ items, total: items.length }) as T
  }

  const jobDetailMatch = pathname.match(/^\/jobs\/([^/]+)$/)
  if (jobDetailMatch && method === 'GET') {
    const job = jobs.find((item) => item.id === decodeURIComponent(jobDetailMatch[1]))
    if (!job) throw new DemoApiError(404, '任务不存在')
    return clone(job) as T
  }

  const retryMatch = pathname.match(/^\/jobs\/([^/]+)\/retry$/)
  if (retryMatch && method === 'POST') {
    const source = jobs.find((job) => job.id === decodeURIComponent(retryMatch[1]))
    if (!source) throw new DemoApiError(404, '任务不存在')
    const job = createJob(source.type, source.failedCount || source.targetCount || 1, `重试任务 ${source.id}`)
    return clone({ job } satisfies JobMutationResult) as T
  }

  throw new DemoApiError(404, `演示接口不存在：${method} ${pathname}`)
}
