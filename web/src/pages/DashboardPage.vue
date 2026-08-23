<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertCircle,
  CircleAlert,
  Clock3,
  PlayCircle,
  RefreshCw,
  Server as ServerIcon,
  UsersRound,
  Wifi,
} from 'lucide-vue-next'

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

const stats = computed(() => [
  { label: '服务器总数', value: data.value?.stats.totalServers ?? '—', meta: '已登记节点', icon: ServerIcon, tone: 'neutral' },
  { label: '在线节点', value: data.value?.stats.onlineServers ?? '—', meta: data.value ? `${Math.round((data.value.stats.onlineServers / Math.max(1, data.value.stats.totalServers)) * 100)}% 可连接` : '连通性', icon: Wifi, tone: 'success' },
  { label: '运行服务', value: data.value?.stats.runningServices ?? '—', meta: '3proxy active', icon: PlayCircle, tone: 'info' },
  { label: '代理用户', value: data.value?.stats.totalUsers ?? '—', meta: '中央账号', icon: UsersRound, tone: 'violet' },
  { label: '失败任务', value: data.value?.stats.failedJobs ?? '—', meta: '需要处理', icon: CircleAlert, tone: data.value?.stats.failedJobs ? 'danger' : 'neutral' },
])

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
        <RefreshCw :size="16" :class="{ spinning: refreshing }" />
        刷新
      </button>
    </header>

    <div v-if="error && !data" class="error-panel">
      <AlertCircle :size="20" />
      <div><strong>无法加载概览</strong><p>{{ error }}</p></div>
      <button class="button secondary compact" type="button" @click="load()">重试</button>
    </div>

    <section class="metric-grid" aria-label="运行指标">
      <article v-for="stat in stats" :key="stat.label" class="metric-item">
        <span class="metric-icon" :class="`metric-${stat.tone}`"><component :is="stat.icon" :size="19" /></span>
        <div class="metric-copy">
          <span>{{ stat.label }}</span>
          <strong>{{ stat.value }}</strong>
          <small>{{ stat.meta }}</small>
        </div>
      </article>
    </section>

    <div class="dashboard-grid">
      <section class="surface-panel server-health-panel">
        <header class="panel-heading">
          <div><h3>节点健康</h3><p>最近上报的连接与服务状态</p></div>
          <RouterLink class="text-link" to="/servers">查看全部</RouterLink>
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
                <td>{{ server.userCount ?? '—' }}</td>
                <td><span :title="formatDateTime(server.lastSeenAt)">{{ formatRelativeTime(server.lastSeenAt) }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <EmptyState v-else title="尚未登记服务器" description="添加服务器后，节点状态会显示在这里。">
          <RouterLink class="button primary compact" to="/servers">添加服务器</RouterLink>
        </EmptyState>
      </section>

      <section class="surface-panel recent-jobs-panel">
        <header class="panel-heading">
          <div><h3>近期任务</h3><p>部署与配置变更记录</p></div>
          <RouterLink class="text-link" to="/jobs">查看全部</RouterLink>
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
            <div class="job-progress-line">
              <ProgressBar :value="job.progress" :tone="job.status === 'failed' ? 'danger' : job.status === 'partially_failed' ? 'warning' : 'default'" />
              <span>{{ job.progress ?? 0 }}%</span>
            </div>
            <div class="job-row-meta"><span><Clock3 :size="13" />{{ formatRelativeTime(job.createdAt) }}</span><span>{{ formatJobMessage(job.message, job.successCount, job.targetCount) }}</span></div>
          </article>
        </div>
        <EmptyState v-else title="暂无任务记录" description="部署或配置变更后，任务会显示在这里。" />
      </section>
    </div>
  </div>
</template>
