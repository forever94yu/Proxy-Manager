import { demoRequest, DemoApiError } from '@/api/demo'
import type { ApiErrorPayload } from '@/types'

const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '')
const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'

export const UNAUTHORIZED_EVENT = 'pm:unauthorized'

interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
}

interface ApiEnvelope<T> {
  data: T
}

export class ApiError extends Error {
  status: number
  code?: string
  fields?: Record<string, string>

  constructor(status: number, message: string, code?: string, fields?: Record<string, string>) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
  }
}

function buildUrl(path: string): string {
  return `${API_BASE_URL}${path.startsWith('/') ? path : `/${path}`}`
}

// The management API answers in English. Everything the UI shows is Chinese,
// so known messages are translated here; unknown ones fall through unchanged
// rather than being hidden.
const MESSAGE_TRANSLATIONS: Record<string, string> = {
  'Username or password is incorrect': '账号或密码错误',
  'Too many failed login attempts; try again later': '登录失败次数过多，请稍后再试',
  'Authentication is required': '登录状态已失效，请重新登录',
  'Origin is not allowed': '请求来源不被允许',
  'Database is unavailable': '数据库暂不可用',
  'The operation could not be completed': '操作未能完成，请稍后重试',
  'Resource not found': '请求的资源不存在',
  'Method not allowed': '不支持该请求方式',
  'Server not found': '服务器不存在或已被删除',
  'Proxy user not found': '代理用户不存在或已被删除',
  'Job not found': '任务不存在',
  'One or more selected servers do not exist': '所选服务器中有已被删除的节点，请刷新后重试',
  'A server with this name already exists': '已存在同名服务器',
  'A proxy user with this username already exists': '已存在同名代理用户',
  'Server has queued or running jobs': '该服务器有排队或执行中的任务，请等待完成后再操作',
  'Server settings cannot be changed while it has queued or running jobs': '该服务器有排队或执行中的任务，暂时不能修改配置',
  'Server settings changed while the operation was being queued; reload and try again': '服务器配置已被修改，请刷新后重试',
  'Proxy user changed while this edit was being prepared; reload and try again': '该代理用户已被其他操作修改，请刷新后重试',
  'Proxy user changed before it could be deleted; reload and try again': '该代理用户已被其他操作修改，请刷新后重试',
  'Job cannot be retried: it is not failed, or newer changes to the same servers or users supersede it': '该任务无法重试：任务未失败，或相关服务器/用户已有更新的变更',
  'Server details are invalid': '服务器信息有误，请检查标注的字段',
  'Proxy user details are invalid': '代理用户信息有误，请检查标注的字段',
  'Proxy user state is invalid': '代理用户状态参数无效',
  'Service action is invalid': '服务操作无效',
  'Server selection is invalid': '服务器选择无效',
  'Server status filter is invalid': '状态筛选条件无效',
  'Sync status filter is invalid': '同步状态筛选条件无效',
  'User status filter is invalid': '使用状态筛选条件无效',
  'Job status filter is invalid': '任务状态筛选条件无效',
  'Job type filter is invalid': '任务类型筛选条件无效',
  'search query is too long': '搜索内容过长',
  'A JSON request body is required': '请求内容为空',
  'Content-Type must be application/json': '请求格式错误',
  'The request body must contain exactly one JSON object': '请求格式错误',
  'This operation accepts only an empty JSON object': '请求格式错误',
  'The request body is empty': '请求内容为空',
  'The request body exceeds 1 MiB': '提交的内容过大（超过 1 MiB）',
  'Invalid JSON request body': '请求格式错误',
  'A field has the wrong type': '字段类型错误',
  'Online updates are disabled': '在线升级已关闭',
  'An update is already in progress': '已有升级正在进行',
  'The requested version is not the latest release; check for updates again': '该版本不是最新版本，请重新检查更新',
  'Proxy Manager is already running the latest version': '当前已是最新版本',
  'The release has no package for this platform': '该版本没有提供适用于当前平台的安装包',
  'Jobs are running; wait for them to finish before updating': '有任务正在执行，请等待完成后再升级',
  'Update version is invalid': '升级版本号无效',
}

const FIELD_TRANSLATIONS: Record<string, string> = {
  'Name must contain 1 to 64 characters': '名称长度需为 1–64 个字符',
  'Host must be a valid IP address or DNS hostname': '请输入有效的 IP 地址或域名',
  'SSH port must be between 1 and 65535': 'SSH 端口范围为 1–65535',
  'SSH user has an invalid format': '请输入有效的 SSH 用户名',
  'Authentication method must be key or password': '认证方式必须为密钥或密码',
  'SSH credential is required': '请填写 SSH 凭据',
  'SSH credential is required when changing authentication method': '切换认证方式时需要重新填写凭据',
  'SSH credential is too large': 'SSH 凭据过大',
  'Private key must be PEM or OpenSSH private key data': '私钥必须是 PEM 或 OpenSSH 格式',
  'HTTP port must be between 1 and 65535': 'HTTP 端口范围为 1–65535',
  'SOCKS port must be between 1 and 65535': 'SOCKS 端口范围为 1–65535',
  'SOCKS and HTTP ports must be different': 'SOCKS 端口不能与 HTTP 端口相同',
  'Provide one or two DNS resolver IP addresses': '请输入 1–2 个 DNS 地址',
  'Every DNS resolver must be an IPv4 address': 'DNS 地址必须是 IPv4',
  'No more than 20 tags are allowed': '标签不能超过 20 个',
  'Tags must contain 1 to 40 letters, digits, or . _ : / - characters': '每个标签 1–40 个字符，仅支持字母、数字及 . _ : / -',
  'Username must contain 1 to 64 letters, digits, underscores, or dashes': '用户名需为 1–64 位字母、数字、下划线或短横线',
  'Password mode is invalid': '密码模式无效',
  'Password must contain 8 to 128 letters, digits, or _ @ % + = , . ! ? - characters': '请输入 8–128 位字母、数字或 _@%+=,.!?-',
  'Password must be omitted unless passwordMode is custom': '仅自定义密码模式可以填写密码',
  'Select at least one server': '至少选择一台服务器',
  'No more than 500 servers may be selected': '最多选择 500 台服务器',
  'Traffic limit must be between 0 and 1 PiB': '流量额度需在 0 到 1 PiB 之间',
  'Expiry time must be an RFC 3339 timestamp': '到期时间格式无效',
  'Reset period must be none, daily, weekly, or monthly': '重置周期无效',
  'Reset anchor must be an RFC 3339 timestamp': '周期起点格式无效',
  'Reset anchor is required for periodic resets': '请设置周期起点',
  'A server identifier is invalid': '服务器标识无效',
  'Use start, stop, or restart': '仅支持启动、停止或重启',
  'Invalid status': '状态无效',
  'Invalid sync status': '同步状态无效',
  'Enabled must be true or false': '启用状态无效',
  'Type is too long': '类型过长',
  'Version must look like 1.4.0': '版本号格式应为 1.4.0',
}

export function translateMessage(message: string): string {
  const exact = MESSAGE_TRANSLATIONS[message] ?? FIELD_TRANSLATIONS[message]
  if (exact) return exact
  if (message.startsWith('Malformed JSON')) return '请求格式错误'
  const wrongType = /^Field (.+) has the wrong type$/.exec(message)
  if (wrongType) return `字段 ${wrongType[1]} 类型错误`
  if (message.startsWith('Unknown field ')) return `不支持的字段 ${message.slice('Unknown field '.length)}`
  return message
}

function translateFields(fields?: Record<string, string>): Record<string, string> | undefined {
  if (!fields) return undefined
  return Object.fromEntries(Object.entries(fields).map(([key, value]) => [key, translateMessage(value)]))
}

function errorMessage(status: number, payload?: ApiErrorPayload): string {
  if (payload?.error?.message) return translateMessage(payload.error.message)
  if (payload?.message) return translateMessage(payload.message)
  if (status === 401) return '登录状态已失效，请重新登录'
  if (status === 403) return '当前账号没有执行此操作的权限'
  if (status === 404) return '请求的资源不存在'
  if (status === 409) return '数据已发生变化，请刷新后重试'
  if (status >= 500) return '服务暂时不可用，请稍后重试'
  return '请求失败，请检查输入后重试'
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = (options.method || 'GET').toUpperCase()
  const url = buildUrl(path)

  if (DEMO_MODE) {
    try {
      return await demoRequest<T>(url, method, options.body)
    } catch (error) {
      if (error instanceof DemoApiError) throw new ApiError(error.status, error.message)
      throw error
    }
  }

  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')

  let response: Response
  try {
    response = await fetch(url, {
      ...options,
      method,
      credentials: 'include',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    })
  } catch {
    throw new ApiError(0, '无法连接管理服务，请检查网络或服务状态')
  }

  const payload = response.status === 204
    ? undefined
    : await response.json().catch(() => undefined) as ApiEnvelope<T> | ApiErrorPayload | undefined

  if (!response.ok) {
    const errorPayload = payload as ApiErrorPayload | undefined
    // Session expired or was revoked: let the app reset auth state and go to
    // the login page. Auth endpoints handle their own 401s (bad credentials /
    // bootstrap probe), so they are excluded.
    if (response.status === 401 && !path.replace(/^\//, '').startsWith('auth/')) {
      window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT))
    }
    throw new ApiError(
      response.status,
      errorMessage(response.status, errorPayload),
      errorPayload?.error?.code,
      translateFields(errorPayload?.error?.fields),
    )
  }

  if (payload && 'data' in payload) return payload.data
  return undefined as T
}

export function queryString(params: Record<string, string | undefined>): string {
  const query = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value) query.set(key, value)
  })
  const serialized = query.toString()
  return serialized ? `?${serialized}` : ''
}
