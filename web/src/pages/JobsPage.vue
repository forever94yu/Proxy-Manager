<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AlertCircle,
  Eye,
  ListChecks,
  RefreshCw,
  RotateCcw,
  SearchCheck,
  Server,
  UserRound,
} from 'lucide-vue-next'

import { jobsApi } from '@/api'
import EmptyState from '@/components/EmptyState.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import { useToastStore } from '@/stores/toast'
import type { Job } from '@/types'
import { compactId, formatDateTime, formatJobMessage, formatRelativeTime } from '@/utils'

const toast = useToastStore()
const jobs = ref<Job[]>([])
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const status = ref('')
const type = ref('')
const retryingId = ref('')
const selectedJob = ref<Job | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let refreshTimer: number | undefined

const jobLabels: Record<string, string> = {
  deploy: '部署服务',
  user_create: '创建代理用户',
  user_update: '更新代理用户',
  user_delete: '删除代理用户',
  user_policy: '同步用户策略',
  user_traffic_reset: '重置流量',
  user_enable: '启用代理用户',
  user_disable: '停用代理用户',
  service_start: '启动服务',
  service_stop: '停止服务',
  service_restart: '重启服务',
  connection_test: '连接测试',
}

const summary = computed(() => ({
  running: jobs.value.filter((job) => job.status === 'running' || job.status === 'queued').length,
  succeeded: jobs.value.filter((job) => job.status === 'succeeded').length,
  failed: jobs.value.filter((job) => job.status === 'failed' || job.status === 'partially_failed').length,
}))

function jobIcon(typeName: string) {
  if (typeName.startsWith('user_')) return UserRound
  if (typeName === 'connection_test') return SearchCheck
  if (typeName.startsWith('service_') || typeName === 'deploy') return Server
  return ListChecks
}

function duration(job: Job): string {
  if (!job.startedAt) return '—'
  const end = job.finishedAt ? new Date(job.finishedAt).getTime() : Date.now()
  const seconds = Math.max(0, Math.round((end - new Date(job.startedAt).getTime()) / 1000))
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  return `${minutes} 分 ${seconds % 60} 秒`
}

async function load(silent = false): Promise<void> {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    jobs.value = await jobsApi.list({ status: status.value, type: type.value })
    if (selectedJob.value) {
      const summary = jobs.value.find((job) => job.id === selectedJob.value?.id)
      if (summary) selectedJob.value = { ...selectedJob.value, ...summary, targets: selectedJob.value.targets }
    }
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '任务记录加载失败'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

async function openDetails(job: Job): Promise<void> {
  selectedJob.value = { ...job, targets: undefined }
  detailLoading.value = true
  detailError.value = ''
  try {
    const detail = await jobsApi.get(job.id)
    if (selectedJob.value?.id === job.id) selectedJob.value = detail
  } catch (caught) {
    detailError.value = caught instanceof Error ? caught.message : '任务详情加载失败'
  } finally {
    detailLoading.value = false
  }
}

async function retry(job: Job): Promise<void> {
  retryingId.value = job.id
  try {
    const result = await jobsApi.retry(job.id)
    toast.notify('success', '失败目标已重新排队', `任务 #${compactId(result.job?.id || job.id)}`)
    selectedJob.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', '重试失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    retryingId.value = ''
  }
}

watch([status, type], () => void load())
onMounted(() => {
  void load()
  refreshTimer = window.setInterval(() => void load(true), 8_000)
})
onBeforeUnmount(() => window.clearInterval(refreshTimer))
</script>

<template>
  <div class="page-stack">
    <header class="page-heading-row">
      <div class="page-heading">
        <h2>任务记录</h2>
      </div>
      <button class="button secondary compact" type="button" :disabled="refreshing" @click="load(true)">
        <RefreshCw :size="16" :class="{ spinning: refreshing }" />刷新
      </button>
    </header>

    <div class="inline-summary" aria-label="任务摘要">
      <span><i class="summary-dot info" />{{ summary.running }} 个执行中</span>
      <span><i class="summary-dot success" />{{ summary.succeeded }} 个已完成</span>
      <span><i class="summary-dot danger" />{{ summary.failed }} 个需要处理</span>
      <span class="summary-note">每 8 秒自动刷新</span>
    </div>

    <section class="surface-panel table-panel">
      <div class="table-toolbar jobs-toolbar">
        <div class="toolbar-title"><ListChecks :size="17" /><span>执行记录</span></div>
        <div class="toolbar-actions">
          <select v-model="type" class="filter-select" aria-label="任务类型筛选">
            <option value="">全部类型</option>
            <option value="deploy">部署服务</option>
            <option value="connection_test">连接测试</option>
            <option value="service_start">启动服务</option>
            <option value="service_stop">停止服务</option>
            <option value="service_restart">重启服务</option>
            <option value="user_create">创建代理用户</option>
            <option value="user_update">更新代理用户</option>
            <option value="user_delete">删除代理用户</option>
            <option value="user_policy">同步用户策略</option>
            <option value="user_traffic_reset">重置流量</option>
            <option value="user_enable">启用代理用户</option>
            <option value="user_disable">停用代理用户</option>
          </select>
          <select v-model="status" class="filter-select" aria-label="任务状态筛选">
            <option value="">全部状态</option>
            <option value="queued">排队中</option>
            <option value="running">运行中</option>
            <option value="succeeded">已完成</option>
            <option value="partially_failed">部分失败</option>
            <option value="failed">失败</option>
            <option value="cancelled">已取消</option>
          </select>
        </div>
      </div>

      <div v-if="error" class="inline-error">
        <AlertCircle :size="17" /><span>{{ error }}</span><button type="button" @click="load()">重试</button>
      </div>
      <TableSkeleton v-if="loading" :rows="7" />
      <div v-else-if="jobs.length" class="table-scroll">
        <table class="data-table jobs-table">
          <thead><tr><th>任务</th><th>状态</th><th>进度</th><th class="hide-mobile">目标结果</th><th class="hide-tablet">操作者</th><th>创建时间</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="job in jobs" :key="job.id">
              <td>
                <div class="job-identity-cell">
                  <span class="job-type-icon"><component :is="jobIcon(job.type)" :size="16" /></span>
                  <div class="primary-cell"><strong>{{ jobLabels[job.type] || job.type }}</strong><span class="mono">#{{ compactId(job.id) }}</span></div>
                </div>
              </td>
              <td><StatusBadge :status="job.status" /></td>
              <td>
                <div class="table-progress">
                  <ProgressBar :value="job.progress" :tone="job.status === 'failed' ? 'danger' : job.status === 'partially_failed' ? 'warning' : 'default'" />
                  <span>{{ job.progress ?? 0 }}%</span>
                </div>
              </td>
              <td class="hide-mobile">
                <span class="target-result"><b class="result-success">{{ job.successCount || 0 }}</b> / {{ job.targetCount || 0 }}<b v-if="job.failedCount" class="result-failed">{{ job.failedCount }} 失败</b></span>
              </td>
              <td class="hide-tablet">{{ job.actor === 'system' || !job.actor ? '系统' : job.actor }}</td>
              <td><span :title="formatDateTime(job.createdAt)">{{ formatRelativeTime(job.createdAt) }}</span></td>
              <td>
                <div class="row-actions">
                  <button class="icon-button" type="button" aria-label="查看任务详情" title="查看详情" @click="openDetails(job)"><Eye :size="16" /></button>
                  <button
                    v-if="job.status === 'failed' || job.status === 'partially_failed'"
                    class="icon-button"
                    type="button"
                    aria-label="重试失败目标"
                    title="重试失败目标"
                    :disabled="retryingId === job.id"
                    @click="retry(job)"
                  ><RotateCcw :size="16" :class="{ spinning: retryingId === job.id }" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else title="没有任务记录" description="当前筛选条件下没有部署或配置变更任务。" />
    </section>

    <ModalDialog
      :open="Boolean(selectedJob)"
      :title="jobLabels[selectedJob?.type || ''] || selectedJob?.type || '任务详情'"
      :description="selectedJob ? `任务 #${selectedJob.id}` : ''"
      size="medium"
      @close="selectedJob = null"
    >
      <div v-if="selectedJob" class="job-detail">
        <div class="job-detail-status">
          <StatusBadge :status="selectedJob.status" />
          <span>{{ formatJobMessage(selectedJob.message, selectedJob.successCount, selectedJob.targetCount) }}</span>
        </div>
        <div class="detail-progress-block">
          <div><span>整体进度</span><strong>{{ selectedJob.progress ?? 0 }}%</strong></div>
          <ProgressBar :value="selectedJob.progress" :tone="selectedJob.status === 'failed' ? 'danger' : selectedJob.status === 'partially_failed' ? 'warning' : 'default'" />
        </div>
        <dl class="detail-grid">
          <div><dt>目标数</dt><dd>{{ selectedJob.targetCount || 0 }}</dd></div>
          <div><dt>成功</dt><dd class="result-success">{{ selectedJob.successCount || 0 }}</dd></div>
          <div><dt>失败</dt><dd :class="{ 'result-failed': selectedJob.failedCount }">{{ selectedJob.failedCount || 0 }}</dd></div>
          <div><dt>执行耗时</dt><dd>{{ duration(selectedJob) }}</dd></div>
          <div><dt>操作者</dt><dd>{{ selectedJob.actor === 'system' || !selectedJob.actor ? '系统' : selectedJob.actor }}</dd></div>
          <div><dt>创建时间</dt><dd>{{ formatDateTime(selectedJob.createdAt) }}</dd></div>
          <div><dt>开始时间</dt><dd>{{ formatDateTime(selectedJob.startedAt) }}</dd></div>
          <div><dt>完成时间</dt><dd>{{ formatDateTime(selectedJob.finishedAt) }}</dd></div>
        </dl>

        <section class="job-target-section">
          <div class="target-section-heading">
            <div><h3>目标节点</h3><p>每台服务器的独立执行结果</p></div>
            <span>{{ selectedJob.targets?.length ?? selectedJob.targetCount ?? 0 }} 个目标</span>
          </div>
          <div v-if="detailLoading" class="target-detail-loading">
            <div v-for="row in 3" :key="row"><span /><span /><span /></div>
          </div>
          <div v-else-if="detailError" class="target-detail-error"><AlertCircle :size="16" />{{ detailError }}</div>
          <div v-else-if="selectedJob.targets?.length" class="job-target-list">
            <article v-for="target in selectedJob.targets" :key="target.serverId" class="job-target-row">
              <StatusBadge :status="target.status" compact />
              <div class="target-identity">
                <strong>{{ target.serverName || target.serverId }}</strong>
                <span class="mono">{{ target.serverId }}</span>
              </div>
              <span class="target-attempt">尝试 {{ target.attempt || 1 }} 次</span>
              <div class="target-outcome" :class="{ failed: target.error }">
                <span v-if="target.error">{{ target.error }}</span>
                <span v-else>{{ target.finishedAt ? `完成于 ${formatDateTime(target.finishedAt)}` : target.startedAt ? `开始于 ${formatDateTime(target.startedAt)}` : '等待执行' }}</span>
              </div>
            </article>
          </div>
          <p v-else class="target-list-empty">该任务没有可显示的目标节点明细。</p>
        </section>
      </div>
      <template #footer>
        <button class="button secondary" type="button" @click="selectedJob = null">关闭</button>
        <button
          v-if="selectedJob && (selectedJob.status === 'failed' || selectedJob.status === 'partially_failed')"
          class="button primary"
          type="button"
          :disabled="retryingId === selectedJob.id"
          @click="retry(selectedJob)"
        >
          <RotateCcw :size="16" />{{ retryingId === selectedJob.id ? '正在重试' : '重试失败目标' }}
        </button>
      </template>
    </ModalDialog>
  </div>
</template>
