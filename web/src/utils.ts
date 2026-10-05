export function formatDateTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'

  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

export function formatRelativeTime(value?: string): string {
  if (!value) return '从未'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '未知'

  const seconds = Math.round((date.getTime() - Date.now()) / 1000)
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })
  if (Math.abs(seconds) < 60) return formatter.format(seconds, 'second')

  const minutes = Math.round(seconds / 60)
  if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute')

  const hours = Math.round(minutes / 60)
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour')

  const days = Math.round(hours / 24)
  return formatter.format(days, 'day')
}

export function compactId(value: string): string {
  return value.length > 10 ? value.slice(0, 8) : value
}

export function formatJobMessage(message?: string, successCount = 0, targetCount = 0): string {
  if (!message) return targetCount ? `${successCount}/${targetCount} 个目标` : '任务状态已更新'

  const completed = /^Completed on (\d+) of (\d+) servers$/.exec(message)
  if (completed) return `已在 ${completed[1]} / ${completed[2]} 台服务器完成`

  const systemMessages: Record<string, string> = {
    'Operation completed': '操作已完成',
    'Operation is running': '任务执行中',
    'Retry queued': '失败目标已重新排队',
    'Recovered after worker restart': 'Worker 重启后已恢复排队',
    'Stale target reconciliation queued': '超时目标已重新排队',
    'Proxy user policy synchronization queued': '用户策略同步已排队',
  }
  return systemMessages[message] || message
}

export function operatorInitials(name: string): string {
  const normalized = name.trim()
  if (!normalized) return 'PM'
  return normalized.slice(0, 2).toUpperCase()
}

export function clampProgress(value?: number): number {
  if (value === undefined || Number.isNaN(value)) return 0
  return Math.max(0, Math.min(100, value))
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/** Formats a byte count with 1024-based units, e.g. 1.5 GB. */
export function formatBytes(value?: number): string {
  if (value === undefined || !Number.isFinite(value) || value <= 0) return '0 B'
  let unit = 0
  let scaled = value
  while (scaled >= 1024 && unit < BYTE_UNITS.length - 1) {
    scaled /= 1024
    unit += 1
  }
  const digits = unit === 0 || scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2
  return `${Number(scaled.toFixed(digits))} ${BYTE_UNITS[unit]}`
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

/** ISO timestamp -> value of an <input type="datetime-local"> in local time. */
export function toLocalInputValue(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/**
 * Value of an <input type="datetime-local"> -> RFC 3339 with the local UTC
 * offset. The server evaluates reset schedules in this fixed offset.
 */
export function fromLocalInputValue(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const offset = -date.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const absolute = Math.abs(offset)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:00${sign}${pad(Math.floor(absolute / 60))}:${pad(absolute % 60)}`
}

export function formatFullDateTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

const WEEKDAYS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

/** Human-readable reset rule, e.g. "每月 15 日 10:00 重置". */
export function describeResetRule(period: string, anchor?: string): string {
  if (period === 'none' || !anchor) return '不自动重置'
  const date = new Date(anchor)
  if (Number.isNaN(date.getTime())) return '不自动重置'
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  if (period === 'daily') return `每天 ${time} 重置`
  if (period === 'weekly') return `每${WEEKDAYS[date.getDay()]} ${time} 重置`
  const day = date.getDate()
  return day > 28 ? `每月 ${day} 日 ${time} 重置（小月为月末）` : `每月 ${day} 日 ${time} 重置`
}

/** Remaining time until a future instant, e.g. "剩余 3 天". */
export function describeRemaining(value?: string): string {
  if (!value) return ''
  const milliseconds = new Date(value).getTime() - Date.now()
  if (Number.isNaN(milliseconds)) return ''
  if (milliseconds <= 0) return '已到期'
  const hours = milliseconds / 3_600_000
  if (hours < 1) return `剩余 ${Math.max(1, Math.round(milliseconds / 60_000))} 分钟`
  if (hours < 48) return `剩余 ${Math.round(hours)} 小时`
  return `剩余 ${Math.floor(hours / 24)} 天`
}

/**
 * Copies text to the clipboard. The async Clipboard API only exists in secure
 * contexts (HTTPS or localhost); the console is often served over plain HTTP,
 * so fall back to a temporary textarea and execCommand('copy').
 */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission denied or document not focused: try the legacy path.
    }
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.top = '0'
  textarea.style.left = '0'
  textarea.style.opacity = '0'
  textarea.style.pointerEvents = 'none'
  const activeElement = document.activeElement as HTMLElement | null
  document.body.appendChild(textarea)
  try {
    textarea.select()
    textarea.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    textarea.remove()
    activeElement?.focus({ preventScroll: true })
  }
}

/** Host as it must appear in an address or URL: bare IPv6 literals get brackets. */
export function formatHost(host: string): string {
  return host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
}

/** "host:port" with IPv6 literals bracketed, e.g. [2001:db8::1]:3128. */
export function formatHostPort(host: string, port: number): string {
  return `${formatHost(host)}:${port}`
}

export type ProxyScheme = 'http' | 'socks5'

/**
 * Proxy URL such as http://alice:secret@203.0.113.10:3128. The username and
 * password are percent-encoded because custom passwords may contain @ % + etc.
 * `masked` replaces the password with dots for on-screen display.
 */
export function buildProxyUri(scheme: ProxyScheme, username: string, password: string, host: string, port: number, masked = false): string {
  const secret = masked ? '••••••' : encodeURIComponent(password)
  return `${scheme}://${encodeURIComponent(username)}:${secret}@${formatHostPort(host, port)}`
}

/** Standard base64 of a UTF-8 string. */
function base64Encode(value: string): string {
  let binary = ''
  for (const byte of new TextEncoder().encode(value)) binary += String.fromCharCode(byte)
  return btoa(binary)
}

/**
 * SOCKS5 node link in the format v2rayN and v2rayNG export, e.g.
 * socks://YWxpY2U6c2VjcmV0@203.0.113.10:1080#Tokyo. The user info is
 * base64(user:pass): standard alphabet, no padding, percent-encoded. v2rayNG,
 * v2rayN and NekoBox import it from a QR code. shareLink in
 * server/subscription.go builds identical links for subscriptions.
 */
export function buildNodeShareLink(name: string, username: string, password: string, host: string, port: number): string {
  const userInfo = encodeURIComponent(base64Encode(`${username}:${password}`).replace(/=+$/, ''))
  return `socks://${userInfo}@${formatHostPort(host, port)}#${encodeURIComponent(name)}`
}

/**
 * SOCKS5 node link in the format subscription panels serve to Shadowrocket:
 * socks://base64(user:pass@host:port)?method=auto#Tokyo.
 */
export function buildShadowrocketNodeLink(name: string, username: string, password: string, host: string, port: number): string {
  return `socks://${base64Encode(`${username}:${password}@${formatHostPort(host, port)}`)}?method=auto#${encodeURIComponent(name)}`
}

/**
 * Subscription URL wrapped in Shadowrocket's add-subscription scheme; its
 * scanner silently ignores a plain subscription URL. The iPhone camera opens
 * Shadowrocket directly for this link. The URL must be standard (not URL-safe)
 * base64 or Shadowrocket ignores the code.
 */
export function buildShadowrocketSubscriptionLink(url: string, remark: string): string {
  return `shadowrocket://add/sub/${base64Encode(url)}?remark=${encodeURIComponent(remark)}`
}

/** Absolute subscription URL; falls back to the origin the console runs on. */
export function resolveSubscriptionUrl(subscription: { path: string; url?: string }): string {
  return subscription.url || new URL(subscription.path, window.location.origin).toString()
}
