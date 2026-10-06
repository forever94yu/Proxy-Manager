<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  AlertCircle,
  CircleArrowUp,
  CircleCheck,
  ExternalLink,
  RefreshCw,
  RotateCcw,
  TriangleAlert,
} from 'lucide-vue-next'

import { systemApi } from '@/api'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import ReleaseNotes from '@/components/ReleaseNotes.vue'
import { useToastStore } from '@/stores/toast'
import type { UpdateStatus } from '@/types'
import { formatBytes, formatFullDateTime, formatRelativeTime } from '@/utils'

const toast = useToastStore()
const status = ref<UpdateStatus | null>(null)
const loading = ref(true)
const checking = ref(false)
const error = ref('')
const confirmOpen = ref(false)
const starting = ref(false)
/** Set while the server restarts into the new release and cannot answer. */
const waitingForRestart = ref(false)
let pollTimer: number | undefined
let restartDeadline = 0

// Covers the up to two minutes the server waits for running jobs.
const RESTART_TIMEOUT_MS = 5 * 60_000

const busy = computed(() => ['downloading', 'installing', 'restarting'].includes(status.value?.phase || '') || waitingForRestart.value)
const latest = computed(() => status.value?.latest)

const progress = computed(() => {
  const value = status.value
  if (!value) return 0
  if (value.phase === 'installing') return 100
  if (!value.totalBytes) return 0
  return Math.round(((value.downloadedBytes || 0) / value.totalBytes) * 100)
})

const phaseLabel = computed(() => {
  if (waitingForRestart.value) return '正在重启，等待新版本上线…'
  switch (status.value?.phase) {
    case 'downloading': return `正在下载 v${status.value.targetVersion}`
    case 'installing': return '正在校验并安装'
    case 'restarting': return '安装完成，正在重启'
    default: return ''
  }
})

const headline = computed(() => {
  const value = status.value
  if (!value) return ''
  if (!value.enabled) return '在线升级未启用'
  if (value.updateAvailable && latest.value) return `发现新版本 v${latest.value.version}`
  if (latest.value) return '已是最新版本'
  return '尚未获取到版本信息'
})

async function load(refresh = false): Promise<void> {
  if (refresh) checking.value = true
  error.value = ''
  try {
    status.value = await systemApi.update(refresh)
    if (refresh && status.value.checkError) toast.notify('error', '检查更新失败', translateUpdateError(status.value.checkError))
    else if (refresh) toast.notify('success', status.value.updateAvailable ? '发现新版本' : '已是最新版本')
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '请稍后重试'
  } finally {
    loading.value = false
    checking.value = false
  }
  schedulePoll()
}

function schedulePoll(): void {
  window.clearTimeout(pollTimer)
  if (busy.value) pollTimer = window.setTimeout(poll, 1_000)
}

async function poll(): Promise<void> {
  const target = status.value?.targetVersion
  try {
    const next = await systemApi.update()
    // The old process keeps answering "restarting" while it waits for
    // running jobs; only a process that has finished starting answers
    // otherwise.
    if (waitingForRestart.value && next.phase !== 'restarting') {
      waitingForRestart.value = false
      status.value = next
      if (target && next.currentVersion === target) {
        toast.notify('success', `已升级到 v${target}`, '页面即将刷新以加载新版控制台')
        window.setTimeout(() => window.location.reload(), 1_500)
      } else {
        toast.notify('error', '升级失败', `v${target} 未能启动，已自动回滚到 v${next.currentVersion}`)
      }
      return
    }
    status.value = next
    if (next.phase === 'restarting' && !waitingForRestart.value) {
      waitingForRestart.value = true
      restartDeadline = Date.now() + RESTART_TIMEOUT_MS
    }
    if (next.phase === 'failed') toast.notify('error', '升级失败', translateUpdateError(next.error))
  } catch {
    // The server is down while it restarts; keep waiting until the deadline.
    if (status.value?.phase === 'restarting' && !waitingForRestart.value) {
      waitingForRestart.value = true
      restartDeadline = Date.now() + RESTART_TIMEOUT_MS
    }
  }
  if (waitingForRestart.value && Date.now() > restartDeadline) {
    waitingForRestart.value = false
    error.value = '服务重启后长时间没有响应，请登录服务器检查服务状态和日志'
    return
  }
  schedulePoll()
}

async function startUpdate(): Promise<void> {
  if (!latest.value) return
  starting.value = true
  try {
    status.value = await systemApi.startUpdate(latest.value.version)
    confirmOpen.value = false
    toast.notify('info', '升级已开始', '下载和安装期间可以离开此页面')
  } catch (cause) {
    toast.notify('error', '无法开始升级', cause instanceof Error ? cause.message : '请稍后重试')
    confirmOpen.value = false
    await load()
  } finally {
    starting.value = false
  }
  schedulePoll()
}

// Errors produced by the updater, see server/updater.go.
const UPDATE_ERRORS: Array<[RegExp, string]> = [
  [/^GitHub could not be reached/, '无法连接 GitHub，请检查控制面服务器的网络或代理设置'],
  [/^No published release was found/, '仓库中还没有已发布的版本'],
  [/^GitHub API rate limit exceeded/, 'GitHub API 请求次数已达上限，请稍后再试'],
  [/^GitHub answered HTTP (\d+)/, 'GitHub 返回 HTTP $1'],
  [/^Release information is invalid/, '版本信息无效'],
  [/^The releases directory is not writable/, '升级目录不可写，请检查数据目录权限'],
  [/^The release does not publish a checksum/, '该版本没有提供安装包校验和'],
  [/^The release checksums are inconsistent/, '版本校验和不一致'],
  [/^The downloaded package does not match its SHA-256 checksum/, '下载的安装包未通过 SHA-256 校验'],
  [/^The release could not be downloaded/, '无法从 GitHub 下载安装包，请检查网络'],
  [/^The release download failed with HTTP (\d+)/, '下载安装包失败（HTTP $1）'],
  [/^The release download was interrupted/, '下载中断，请重试'],
  [/^The release package is too large/, '安装包过大'],
  [/^The release package is not a valid archive/, '安装包已损坏'],
  [/^The release package contains an unexpected path/, '安装包包含异常路径，已拒绝安装'],
  [/^The release package is incomplete: (.+) is missing/, '安装包不完整，缺少 $1'],
  [/^The new program cannot run on this server/, '新版本程序无法在当前服务器上运行'],
  [/^The new program reports version/, '新版本程序的版本号与发布版本不一致'],
  [/^Back up the database/, '备份数据库失败'],
  [/^Install the release/, '安装新版本失败'],
  [/^Save the release state/, '保存版本状态失败'],
]

function translateUpdateError(message?: string): string {
  if (!message) return '未知错误'
  for (const [pattern, translation] of UPDATE_ERRORS) {
    const match = pattern.exec(message)
    if (match) return translation.replace(/\$(\d)/g, (_, group: string) => match[Number(group)] || '')
  }
  return message
}

onMounted(() => load())
onBeforeUnmount(() => window.clearTimeout(pollTimer))
</script>

<template>
  <div class="page-stack">
    <header class="page-heading-row">
      <div class="page-heading">
        <h2>系统更新</h2>
      </div>
      <button
        class="button secondary compact"
        type="button"
        :disabled="checking || busy || loading || !status?.enabled"
        @click="load(true)"
      >
        <RefreshCw :size="15" :class="{ spinning: checking }" />
        检查更新
      </button>
    </header>

    <div v-if="error && !status" class="error-panel">
      <AlertCircle :size="20" />
      <div><strong>无法获取版本信息</strong><p>{{ error }}</p></div>
      <button class="button secondary compact" type="button" @click="load()">重试</button>
    </div>

    <section v-if="loading" class="surface-panel update-panel" aria-busy="true">
      <div class="credential-loading">
        <span class="skeleton-block wide" />
        <span class="skeleton-block" />
        <span class="skeleton-block short" />
      </div>
    </section>

    <template v-else-if="status">
      <section class="surface-panel update-panel">
        <div class="update-hero">
          <span class="update-icon" :class="{ available: status.updateAvailable && status.enabled }">
            <CircleArrowUp v-if="status.updateAvailable && status.enabled" :size="26" />
            <CircleCheck v-else :size="26" />
          </span>
          <div class="update-copy">
            <h3>{{ headline }}</h3>
            <p>
              当前版本 <strong>v{{ status.currentVersion }}</strong>
              <template v-if="status.checkedAt"> · 上次检查 {{ formatRelativeTime(status.checkedAt) }}</template>
            </p>
          </div>
          <button
            v-if="status.enabled && status.updateAvailable && latest"
            class="button primary"
            type="button"
            :disabled="busy || !latest.packageName"
            @click="confirmOpen = true"
          >
            <span v-if="busy" class="button-spinner" />
            {{ busy ? '升级中' : `升级到 v${latest.version}` }}
          </button>
        </div>

        <div v-if="busy" class="update-progress" aria-live="polite">
          <div class="update-progress-label">
            <span>{{ phaseLabel }}</span>
            <span v-if="status.phase === 'downloading' && status.totalBytes" class="mono">
              {{ formatBytes(status.downloadedBytes) }} / {{ formatBytes(status.totalBytes) }}
            </span>
          </div>
          <ProgressBar :value="waitingForRestart || status.phase === 'restarting' ? 100 : progress" />
          <p>升级期间控制台会短暂不可用，代理节点不受影响。完成后页面会自动刷新。</p>
        </div>

        <div v-if="status.phase === 'failed' && status.error" class="update-alert danger">
          <TriangleAlert :size="17" />
          <span>升级到 v{{ status.targetVersion }} 失败：{{ translateUpdateError(status.error) }}。当前版本未受影响，可以重试。</span>
        </div>
        <div v-if="status.failedVersion" class="update-alert warning">
          <RotateCcw :size="17" />
          <span>v{{ status.failedVersion }} 升级后未能启动，已自动回滚到 v{{ status.currentVersion }}。请查看服务日志排查原因。</span>
        </div>
        <div v-if="status.checkError" class="update-alert warning">
          <TriangleAlert :size="17" />
          <span>检查更新失败：{{ translateUpdateError(status.checkError) }}</span>
        </div>
        <div v-if="error && status" class="update-alert danger">
          <AlertCircle :size="17" />
          <span>{{ error }}</span>
        </div>
        <div v-if="!status.enabled" class="update-alert info">
          <AlertCircle :size="17" />
          <span>
            在线升级只在生产环境（<code>APP_ENV=production</code>）默认开启，也可以设置 <code>UPDATE_ENABLED=true</code> 开启。
            Docker 部署请按 README 用 <code>git pull</code> 后重新构建镜像升级。
          </span>
        </div>
        <div v-else-if="status.updateAvailable && latest && !latest.packageName" class="update-alert warning">
          <TriangleAlert :size="17" />
          <span>v{{ latest.version }} 没有提供适用于 {{ status.platform }} 的安装包，请手动升级。</span>
        </div>

        <dl class="detail-grid update-details">
          <div><dt>当前版本</dt><dd class="mono">v{{ status.currentVersion }}</dd></div>
          <div><dt>最新版本</dt><dd class="mono">{{ latest ? `v${latest.version}` : '—' }}</dd></div>
          <div><dt>运行平台</dt><dd class="mono">{{ status.platform }}</dd></div>
          <div><dt>发布时间</dt><dd>{{ latest ? formatFullDateTime(latest.publishedAt) : '—' }}</dd></div>
          <div><dt>更新来源</dt><dd class="mono">{{ status.repository || '—' }}</dd></div>
          <div><dt>安装包大小</dt><dd>{{ latest?.packageSize ? formatBytes(latest.packageSize) : '—' }}</dd></div>
        </dl>
      </section>

      <section v-if="latest" class="surface-panel update-notes-panel">
        <header class="panel-heading">
          <h3>{{ latest.name || `v${latest.version}` }} 更新说明</h3>
          <a class="text-link" :href="latest.url" target="_blank" rel="noopener noreferrer">在 GitHub 查看<ExternalLink :size="14" /></a>
        </header>
        <ReleaseNotes v-if="latest.notes" :source="latest.notes" />
        <p v-else class="release-notes">该版本没有填写更新说明。</p>
      </section>
    </template>

    <ConfirmDialog
      :open="confirmOpen"
      :title="`升级到 v${latest?.version}`"
      description="将从 GitHub 下载新版本并校验 SHA-256，备份数据库后安装，然后自动重启控制台。"
      detail="重启期间控制台会中断约几秒到几十秒，代理节点和代理连接不受影响。新版本启动失败时会自动回滚。"
      confirm-label="开始升级"
      :busy="starting"
      @close="confirmOpen = false"
      @confirm="startUpdate"
    />
  </div>
</template>
