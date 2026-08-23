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
