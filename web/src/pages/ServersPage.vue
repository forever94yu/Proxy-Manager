<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AlertCircle,
  CheckSquare2,
  CirclePlus,
  Pencil,
  Play,
  PlugZap,
  RefreshCw,
  Rocket,
  Search,
  Square,
  Trash2,
} from 'lucide-vue-next'

import { serversApi } from '@/api'
import { ApiError } from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ServerFormDialog from '@/components/ServerFormDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import { useToastStore } from '@/stores/toast'
import type { Server, ServerInput } from '@/types'
import { compactId, formatRelativeTime } from '@/utils'

const toast = useToastStore()
const servers = ref<Server[]>([])
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const search = ref('')
const status = ref('')
const formOpen = ref(false)
const editingServer = ref<Server | null>(null)
const formSubmitting = ref(false)
const formFieldErrors = ref<Record<string, string> | undefined>()
const deleteTarget = ref<Server | null>(null)
const deployTarget = ref<Server | null>(null)
const stopTarget = ref<Server | null>(null)
const confirming = ref(false)
const actionIds = ref<Set<string>>(new Set())
const selectedIds = ref<Set<string>>(new Set())
const bulkConfirmAction = ref<'deploy' | 'stop' | 'restart' | null>(null)
const bulkBusy = ref(false)
let searchTimer: number | undefined
let refreshTimer: number | undefined

const summary = computed(() => ({
  total: servers.value.length,
  online: servers.value.filter((server) => server.status === 'online').length,
  running: servers.value.filter((server) => server.serviceStatus === 'running').length,
}))
const selectedCount = computed(() => selectedIds.value.size)
const allVisibleSelected = computed(() => servers.value.length > 0 && servers.value.every((server) => selectedIds.value.has(server.id)))
const someVisibleSelected = computed(() => !allVisibleSelected.value && servers.value.some((server) => selectedIds.value.has(server.id)))
const bulkDialog = computed(() => {
  const count = selectedCount.value
  if (bulkConfirmAction.value === 'deploy') {
    return {
      title: '批量部署 3proxy',
      description: `将在选中的 ${count} 台服务器上部署或重新部署 3proxy。`,
      detail: '每台服务器会独立执行预检、配置写入和健康检查，结果记录在同一个批量任务中。',
      confirmLabel: '开始批量部署',
      danger: false,
    }
  }
  if (bulkConfirmAction.value === 'stop') {
    return {
      title: '批量停止代理服务',
      description: `停止选中 ${count} 台服务器上的 3proxy 服务？`,
      detail: '目标节点上的现有代理连接将立即中断。',
      confirmLabel: '停止服务',
      danger: true,
    }
  }
  return {
    title: '批量重启代理服务',
    description: `重启选中 ${count} 台服务器上的 3proxy 服务？`,
    detail: '每个节点会出现短暂连接中断，任务将分别记录执行结果。',
    confirmLabel: '重启服务',
    danger: false,
  }
})

function setAction(id: string, active: boolean): void {
  const next = new Set(actionIds.value)
  if (active) next.add(id)
  else next.delete(id)
  actionIds.value = next
}

async function load(silent = false): Promise<void> {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    const items = await serversApi.list({ search: search.value.trim(), status: status.value })
    servers.value = items
    selectedIds.value = new Set([...selectedIds.value].filter((id) => items.some((server) => server.id === id)))
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '服务器列表加载失败'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function toggleServer(id: string, checked: boolean): void {
  const next = new Set(selectedIds.value)
  if (checked) next.add(id)
  else next.delete(id)
  selectedIds.value = next
}

function toggleServerFromEvent(id: string, event: Event): void {
  toggleServer(id, (event.target as HTMLInputElement).checked)
}

function toggleAllFromEvent(event: Event): void {
  const checked = (event.target as HTMLInputElement).checked
  const next = new Set(selectedIds.value)
  servers.value.forEach((server) => checked ? next.add(server.id) : next.delete(server.id))
  selectedIds.value = next
}

function clearSelection(): void {
  selectedIds.value = new Set()
}

function openCreate(): void {
  editingServer.value = null
  formOpen.value = true
}

function openEdit(server: Server): void {
  editingServer.value = server
  formOpen.value = true
}

async function submitServer(input: ServerInput): Promise<void> {
  formSubmitting.value = true
  formFieldErrors.value = undefined
  try {
    const result = editingServer.value
      ? await serversApi.update(editingServer.value.id, input)
      : await serversApi.create(input)
    toast.notify('success', editingServer.value ? '服务器已更新' : '服务器已添加', result.job ? `任务 #${compactId(result.job.id)} 已创建` : undefined)
    formOpen.value = false
    await load(true)
  } catch (caught) {
    if (caught instanceof ApiError && caught.fields) formFieldErrors.value = caught.fields
    toast.notify('error', '保存失败', caught instanceof Error ? caught.message : '请检查输入后重试')
  } finally {
    formSubmitting.value = false
  }
}

async function testConnection(server: Server): Promise<void> {
  setAction(server.id, true)
  try {
    const result = await serversApi.test(server.id)
    toast.notify('info', '连接测试已开始', result.job ? `任务 #${compactId(result.job.id)} 正在后台执行` : result.message)
  } catch (caught) {
    toast.notify('error', '无法开始连接测试', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    setAction(server.id, false)
  }
}

async function deploy(): Promise<void> {
  if (!deployTarget.value) return
  confirming.value = true
  const server = deployTarget.value
  try {
    const result = await serversApi.deploy(server.id)
    toast.notify('success', '部署任务已创建', result.job ? `任务 #${compactId(result.job.id)} · ${server.name}` : server.name)
    deployTarget.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', '部署失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    confirming.value = false
  }
}

async function serviceAction(server: Server, action: 'start' | 'stop' | 'restart'): Promise<void> {
  setAction(server.id, true)
  try {
    const result = await serversApi.service(server.id, action)
    const label = action === 'start' ? '启动' : action === 'stop' ? '停止' : '重启'
    toast.notify('success', `${label}任务已创建`, result.job ? `任务 #${compactId(result.job.id)} · ${server.name}` : server.name)
    await load(true)
  } catch (caught) {
    toast.notify('error', '服务操作失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    setAction(server.id, false)
  }
}

async function runBulk(action: 'deploy' | 'start' | 'stop' | 'restart'): Promise<void> {
  const serverIds = [...selectedIds.value]
  if (!serverIds.length) return
  bulkBusy.value = true
  try {
    const result = action === 'deploy'
      ? await serversApi.deployMany({ serverIds })
      : await serversApi.serviceMany({ serverIds, action })
    const label = action === 'deploy' ? '批量部署' : action === 'start' ? '批量启动' : action === 'stop' ? '批量停止' : '批量重启'
    toast.notify('success', `${label}任务已创建`, result.job ? `任务 #${compactId(result.job.id)} · ${serverIds.length} 台服务器` : `${serverIds.length} 台服务器`)
    bulkConfirmAction.value = null
    clearSelection()
    await load(true)
  } catch (caught) {
    toast.notify('error', '批量操作失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    bulkBusy.value = false
  }
}

function confirmBulk(): void {
  if (bulkConfirmAction.value) void runBulk(bulkConfirmAction.value)
}

async function stopService(): Promise<void> {
  if (!stopTarget.value) return
  confirming.value = true
  const server = stopTarget.value
  await serviceAction(server, 'stop')
  stopTarget.value = null
  confirming.value = false
}

async function removeServer(): Promise<void> {
  if (!deleteTarget.value) return
  confirming.value = true
  try {
    await serversApi.remove(deleteTarget.value.id)
    toast.notify('success', '服务器已从控制台删除', deleteTarget.value.name)
    deleteTarget.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', '删除失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    confirming.value = false
  }
}

watch(search, () => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => void load(), 250)
})
watch(status, () => void load())
onMounted(() => {
  void load()
  refreshTimer = window.setInterval(() => void load(true), 8_000)
})
onBeforeUnmount(() => {
  window.clearTimeout(searchTimer)
  window.clearInterval(refreshTimer)
})
</script>

<template>
  <div class="page-stack">
    <header class="page-heading-row">
      <div class="page-heading">
        <h2>服务器</h2>
      </div>
      <button class="button primary" type="button" @click="openCreate"><CirclePlus :size="17" />添加服务器</button>
    </header>

    <div class="inline-summary" aria-label="服务器摘要">
      <span><strong>{{ summary.total }}</strong> 台服务器</span>
      <span><i class="summary-dot success" />{{ summary.online }} 台在线</span>
      <span><i class="summary-dot info" />{{ summary.running }} 个服务运行中</span>
    </div>

    <section class="surface-panel table-panel">
      <div class="table-toolbar">
        <div class="search-control">
          <Search :size="17" />
          <input v-model="search" type="search" placeholder="搜索名称、地址或标签" aria-label="搜索服务器" />
        </div>
        <div class="toolbar-actions">
          <select v-model="status" class="filter-select" aria-label="状态筛选">
            <option value="">全部状态</option>
            <option value="online">在线</option>
            <option value="offline">离线</option>
            <option value="running">服务运行中</option>
            <option value="stopped">服务已停止</option>
            <option value="not_installed">未部署</option>
            <option value="failed">异常</option>
          </select>
          <button class="icon-button bordered" type="button" aria-label="刷新服务器列表" title="刷新" :disabled="refreshing" @click="load(true)">
            <RefreshCw :size="17" :class="{ spinning: refreshing }" />
          </button>
        </div>
      </div>

      <Transition name="fade">
        <div v-if="selectedCount" class="bulk-action-bar">
          <div class="bulk-selection-copy">
            <CheckSquare2 :size="17" />
            <span>已选择 <strong>{{ selectedCount }}</strong> 台服务器</span>
            <button class="text-button" type="button" :disabled="bulkBusy" @click="clearSelection">取消选择</button>
          </div>
          <div class="bulk-buttons">
            <button class="button secondary compact" type="button" :disabled="bulkBusy" @click="bulkConfirmAction = 'deploy'"><Rocket :size="15" />批量部署</button>
            <button class="button secondary compact" type="button" :disabled="bulkBusy" @click="runBulk('start')"><Play :size="15" />启动</button>
            <button class="button secondary compact" type="button" :disabled="bulkBusy" @click="bulkConfirmAction = 'stop'"><Square :size="13" />停止</button>
            <button class="button secondary compact" type="button" :disabled="bulkBusy" @click="bulkConfirmAction = 'restart'"><RefreshCw :size="15" />重启</button>
          </div>
        </div>
      </Transition>

      <div v-if="error" class="inline-error">
        <AlertCircle :size="17" /><span>{{ error }}</span><button type="button" @click="load()">重试</button>
      </div>
      <TableSkeleton v-if="loading" :rows="6" />
      <div v-else-if="servers.length" class="table-scroll">
        <table class="data-table servers-table">
          <thead>
            <tr>
              <th class="selection-column">
                <input
                  class="table-checkbox"
                  type="checkbox"
                  :checked="allVisibleSelected"
                  :indeterminate="someVisibleSelected"
                  aria-label="选择全部当前服务器"
                  @change="toggleAllFromEvent"
                />
              </th>
              <th>服务器</th><th>连接</th><th>3proxy 服务</th><th class="hide-tablet">系统 / 版本</th><th class="hide-mobile">端口</th><th class="hide-mobile">用户</th><th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="server in servers" :key="server.id" :class="{ 'selected-row': selectedIds.has(server.id) }">
              <td class="selection-column">
                <input
                  class="table-checkbox"
                  type="checkbox"
                  :checked="selectedIds.has(server.id)"
                  :aria-label="`选择服务器 ${server.name}`"
                  @change="toggleServerFromEvent(server.id, $event)"
                />
              </td>
              <td>
                <div class="primary-cell server-name-cell">
                  <strong>{{ server.name }}</strong>
                  <span class="mono">{{ server.host }}:{{ server.sshPort }}</span>
                  <div v-if="server.tags?.length" class="tag-row"><span v-for="tag in server.tags.slice(0, 2)" :key="tag" class="mini-tag">{{ tag }}</span></div>
                </div>
              </td>
              <td>
                <div class="status-stack"><StatusBadge :status="server.status" compact /><small>{{ formatRelativeTime(server.lastSeenAt) }}</small></div>
              </td>
              <td>
                <div class="status-stack"><StatusBadge :status="server.installStatus" compact /><StatusBadge v-if="server.installStatus === 'installed'" :status="server.serviceStatus" compact /></div>
              </td>
              <td class="hide-tablet">
                <div class="primary-cell compact-cell"><span>{{ server.os || '待探测' }}</span><small>{{ server.version ? `v${server.version}` : '版本未知' }}</small></div>
              </td>
              <td class="hide-mobile"><span class="mono">{{ server.httpPort || '—' }} / {{ server.socksPort || '—' }}</span></td>
              <td class="hide-mobile">{{ server.userCount ?? '—' }}</td>
              <td>
                <div class="row-actions">
                  <button class="icon-button" type="button" aria-label="测试连接" title="测试连接" :disabled="actionIds.has(server.id)" @click="testConnection(server)">
                    <PlugZap :size="17" />
                  </button>
                  <button v-if="server.installStatus !== 'installed'" class="icon-button accent" type="button" aria-label="部署 3proxy" title="部署 3proxy" :disabled="server.status === 'offline' || actionIds.has(server.id)" @click="deployTarget = server">
                    <Rocket :size="17" />
                  </button>
                  <button v-else-if="server.serviceStatus === 'running'" class="icon-button" type="button" aria-label="重启服务" title="重启服务" :disabled="actionIds.has(server.id)" @click="serviceAction(server, 'restart')">
                    <RefreshCw :size="17" :class="{ spinning: actionIds.has(server.id) }" />
                  </button>
                  <button v-else class="icon-button success" type="button" aria-label="启动服务" title="启动服务" :disabled="actionIds.has(server.id)" @click="serviceAction(server, 'start')">
                    <Play :size="17" />
                  </button>
                  <button v-if="server.installStatus === 'installed' && server.serviceStatus === 'running'" class="icon-button" type="button" aria-label="停止服务" title="停止服务" @click="stopTarget = server"><Square :size="14" /></button>
                  <button class="icon-button" type="button" aria-label="编辑服务器" title="编辑服务器" @click="openEdit(server)"><Pencil :size="16" /></button>
                  <button class="icon-button danger-icon" type="button" aria-label="删除服务器" title="从控制台删除" @click="deleteTarget = server"><Trash2 :size="16" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else :title="search || status ? '没有匹配的服务器' : '尚未添加服务器'" :description="search || status ? '调整搜索关键词或状态筛选后重试。' : '添加第一台服务器以开始部署和服务管理。'">
        <button v-if="!search && !status" class="button primary compact" type="button" @click="openCreate">添加服务器</button>
      </EmptyState>
    </section>

    <ServerFormDialog :open="formOpen" :server="editingServer" :submitting="formSubmitting" :field-errors="formFieldErrors" @close="formOpen = false" @submit="submitServer" />

    <ConfirmDialog
      :open="Boolean(bulkConfirmAction)"
      :title="bulkDialog.title"
      :description="bulkDialog.description"
      :detail="bulkDialog.detail"
      :confirm-label="bulkDialog.confirmLabel"
      :danger="bulkDialog.danger"
      :busy="bulkBusy"
      @close="bulkConfirmAction = null"
      @confirm="confirmBulk"
    />

    <ConfirmDialog
      :open="Boolean(deployTarget)"
      title="部署 3proxy"
      :description="`将在 ${deployTarget?.name || ''} 上安装并启动 3proxy。`"
      detail="部署过程包含依赖检查、配置写入和服务健康检查。"
      confirm-label="开始部署"
      :busy="confirming"
      @close="deployTarget = null"
      @confirm="deploy"
    />
    <ConfirmDialog
      :open="Boolean(stopTarget)"
      title="停止代理服务"
      :description="`停止 ${stopTarget?.name || ''} 上的 3proxy 服务？`"
      detail="该节点上的现有代理连接将立即中断。"
      confirm-label="停止服务"
      danger
      :busy="confirming"
      @close="stopTarget = null"
      @confirm="stopService"
    />
    <ConfirmDialog
      :open="Boolean(deleteTarget)"
      title="删除服务器"
      :description="`从控制台删除 ${deleteTarget?.name || ''}？`"
      detail="该操作只移除管理记录，不会卸载远端 3proxy。相关代理用户的节点绑定也会被移除。"
      confirm-label="删除服务器"
      danger
      :busy="confirming"
      @close="deleteTarget = null"
      @confirm="removeServer"
    />
  </div>
</template>
