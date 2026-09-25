<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { AlertCircle, ChevronRight, RefreshCw } from 'lucide-vue-next'

import { dashboardApi } from '@/api'
import EmptyState from '@/components/EmptyState.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import type { DashboardData } from '@/types'
import { compactId, formatDateTime, formatJobMessage, formatRelativeTime } from '@/utils'

const data = ref<DashboardData | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
let refreshTimer: number | undefined

// Two concentric rings: outer = servers reachable, inner = 3proxy running.
const RING_OUTER = 64
const RING_INNER = 46
const circumference = (radius: number) => 2 * Math.PI * radius

function ratio(part?: number, total?: number): number {
  if (!total) return 0
  return Math.max(0, Math.min(1, (part ?? 0) / total))
}

const stats = computed(() => data.value?.stats)
const onlineRatio = computed(() => ratio(stats.value?.onlineServers, stats.value?.totalServers))
const runningRatio = computed(() => ratio(stats.value?.runningServices, stats.value?.totalServers))
const healthHeadline = computed(() => {
  if (!stats.value) return '正在读取节点状态'
  if (!stats.value.totalServers) return '还没有登记服务器'
  const offline = stats.value.totalServers - stats.value.onlineServers
  if (offline === 0 && stats.value.runningServices === stats.value.totalServers) return '所有节点运行正常'
  if (offline > 0) return `${offline} 台服务器无法连接`
  return `${stats.value.totalServers - stats.value.runningServices} 台服务器的代理服务未运行`
})

function dash(radius: number, value: number): string {
  const length = circumference(radius)
  return `${length * value} ${length}`
}

const jobLabels: Record<string, string> = {
  deploy: '部署服务',
  user_create: '创建用户',
  user_update: '更新用户',
  user_delete: '删除用户',
  service_start: '启动服务',
  service_stop: '停止服务',
  service_restart: '重启服务',
  connection_test: '连接测试',
}

async function load(silent = false): Promise<void> {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    data.value = await dashboardApi.get()
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '概览数据加载失败'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

onMounted(() => {
  void load()
  refreshTimer = window.setInterval(() => void load(true), 30_000)
})
onBeforeUnmount(() => window.clearInterval(refreshTimer))
</script>

<template>
  <div class="page-stack">
    <header class="page-heading-row">
      <div class="page-heading">
        <h2>运行概览</h2>
      </div>
      <button class="button secondary compact" type="button" :disabled="refreshing" @click="load(true)">
        <RefreshCw :size="15" :class="{ spinning: refreshing }" />
        刷新
      </button>
    </header>

    <div v-if="error && !data" class="error-panel">
      <AlertCircle :size="20" />
      <div><strong>无法加载概览</strong><p>{{ error }}</p></div>
      <button class="button secondary compact" type="button" @click="load()">重试</button>
    </div>

    <section class="health-hero" aria-label="节点健康">
      <div class="health-rings" :class="{ ready: Boolean(stats) }">
        <svg viewBox="0 0 160 160" role="img" :aria-label="`在线 ${stats?.onlineServers ?? 0} 台，服务运行 ${stats?.runningServices ?? 0} 台，共 ${stats?.totalServers ?? 0} 台`">
          <circle class="ring-track ring-online" cx="80" cy="80" :r="RING_OUTER" />
          <circle class="ring-value ring-online" cx="80" cy="80" :r="RING_OUTER" :stroke-dasharray="dash(RING_OUTER, stats ? onlineRatio : 0)" />
          <circle class="ring-track ring-running" cx="80" cy="80" :r="RING_INNER" />
          <circle class="ring-value ring-running" cx="80" cy="80" :r="RING_INNER" :stroke-dasharray="dash(RING_INNER, stats ? runningRatio : 0)" />
        </svg>
        <div class="ring-center">
          <strong>{{ stats?.totalServers ?? '—' }}</strong>
          <span>台服务器</span>
        </div>
      </div>

      <div class="health-copy">
        <h3>{{ healthHeadline }}</h3>
        <dl class="health-legend">
          <div class="legend-online">
            <dt>在线节点</dt>
            <dd><strong>{{ stats?.onlineServers ?? '—' }}</strong><span>/ {{ stats?.totalServers ?? '—' }}</span></dd>
          </div>
          <div class="legend-running">
            <dt>服务运行</dt>
            <dd><strong>{{ stats?.runningServices ?? '—' }}</strong><span>/ {{ stats?.totalServers ?? '—' }}</span></dd>
          </div>
          <div>
            <dt>代理用户</dt>
            <dd><strong>{{ stats?.totalUsers ?? '—' }}</strong></dd>
          </div>
          <div :class="{ 'legend-alert': Boolean(stats?.failedJobs) }">
            <dt>失败任务</dt>
            <dd>
              <strong>{{ stats?.failedJobs ?? '—' }}</strong>
              <RouterLink v-if="stats?.failedJobs" class="text-link" to="/jobs">去处理</RouterLink>
            </dd>
          </div>
        </dl>
      </div>
    </section>

    <div class="dashboard-grid">
      <section class="surface-panel server-health-panel">
        <header class="panel-heading">
          <h3>节点</h3>
          <RouterLink class="text-link" to="/servers">全部服务器<ChevronRight :size="15" /></RouterLink>
        </header>
        <TableSkeleton v-if="loading" :rows="5" />
        <div v-else-if="data?.servers.length" class="table-scroll">
          <table class="data-table dashboard-table">
            <thead><tr><th>服务器</th><th>连接</th><th>服务</th><th>用户</th><th>最后上报</th></tr></thead>
            <tbody>
              <tr v-for="server in data.servers" :key="server.id">
                <td>
                  <div class="primary-cell"><strong>{{ server.name }}</strong><span class="mono">{{ server.host }}</span></div>
                </td>
                <td><StatusBadge :status="server.status" compact /></td>
                <td><StatusBadge :status="server.serviceStatus" compact /></td>
                <td class="numeric">{{ server.userCount ?? '—' }}</td>
                <td class="muted-cell"><span :title="formatDateTime(server.lastSeenAt)">{{ formatRelativeTime(server.lastSeenAt) }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <EmptyState v-else title="还没有服务器" description="添加服务器后，这里会显示每台节点的连接和服务状态。">
          <RouterLink class="button primary compact" to="/servers">添加服务器</RouterLink>
        </EmptyState>
      </section>

      <section class="surface-panel recent-jobs-panel">
        <header class="panel-heading">
          <h3>近期任务</h3>
          <RouterLink class="text-link" to="/jobs">全部任务<ChevronRight :size="15" /></RouterLink>
        </header>
        <div v-if="loading" class="job-list-loading">
          <div v-for="row in 5" :key="row" class="job-skeleton"><span /><span /></div>
        </div>
        <div v-else-if="data?.recentJobs.length" class="compact-job-list">
          <article v-for="job in data.recentJobs" :key="job.id" class="compact-job-row">
            <div class="job-row-topline">
              <div class="job-title"><strong>{{ jobLabels[job.type] || job.type }}</strong><span class="mono">#{{ compactId(job.id) }}</span></div>
              <StatusBadge :status="job.status" compact />
            </div>
            <ProgressBar
              v-if="job.status === 'running' || job.status === 'queued'"
              :value="job.progress"
            />
            <div class="job-row-meta">
              <span>{{ formatJobMessage(job.message, job.successCount, job.targetCount) }}</span>
              <span>{{ formatRelativeTime(job.createdAt) }}</span>
            </div>
          </article>
        </div>
        <EmptyState v-else title="暂无任务" description="部署服务器或修改代理用户后，任务会显示在这里。" />
      </section>
    </div>
  </div>
</template>
