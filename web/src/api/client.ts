import { demoRequest, DemoApiError } from '@/api/demo'
import type { ApiErrorPayload } from '@/types'

const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '')
const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'

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

function errorMessage(status: number, payload?: ApiErrorPayload): string {
  if (payload?.error?.message) return payload.error.message
  if (payload?.message) return payload.message
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
    throw new ApiError(
      response.status,
      errorMessage(response.status, errorPayload),
      errorPayload?.error?.code,
      errorPayload?.error?.fields,
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
