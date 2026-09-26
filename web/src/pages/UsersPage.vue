<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AlertCircle,
  CirclePause,
  CirclePlay,
  CirclePlus,
  KeyRound,
  Pencil,
  RefreshCw,
  RotateCcw,
  Search,
  Server as ServerIcon,
  Trash2,
  UsersRound,
} from 'lucide-vue-next'

import { serversApi, usersApi } from '@/api'
import { ApiError } from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import SecretRevealDialog from '@/components/SecretRevealDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import UserFormDialog from '@/components/UserFormDialog.vue'
import { useToastStore } from '@/stores/toast'
import type { ProxyUser, ProxyUserInput, Server } from '@/types'
import {
  compactId,
  describeRemaining,
  describeResetRule,
  formatBytes,
  formatDateTime,
  formatFullDateTime,
  formatRelativeTime,
} from '@/utils'

const toast = useToastStore()
const users = ref<ProxyUser[]>([])
const servers = ref<Server[]>([])
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const search = ref('')
const syncStatus = ref('')
const usageStatus = ref('')
const formOpen = ref(false)
const editingUser = ref<ProxyUser | null>(null)
const formSubmitting = ref(false)
const formFieldErrors = ref<Record<string, string> | undefined>()
const deleteTarget = ref<ProxyUser | null>(null)
const deleting = ref(false)
const resetTarget = ref<ProxyUser | null>(null)
const resetting = ref(false)
const stateTarget = ref<ProxyUser | null>(null)
const changingState = ref(false)
const secretRecord = ref<{ username: string; password: string } | null>(null)
let searchTimer: number | undefined
let refreshTimer: number | undefined

const summary = computed(() => ({
  total: users.value.length,
  synced: users.value.filter((user) => user.syncStatus === 'synced').length,
  attention: users.value.filter((user) => user.syncStatus === 'failed' || user.syncStatus === 'partial').length,
  limited: users.value.filter((user) => user.status === 'expired' || user.status === 'exhausted').length,
  disabled: users.value.filter((user) => user.status === 'disabled').length,
}))

const serverMap = computed(() => new Map(servers.value.map((server) => [server.id, server.name])))
const hasFilters = computed(() => Boolean(search.value || syncStatus.value || usageStatus.value))

async function load(silent = false): Promise<void> {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    const [userItems, serverItems] = await Promise.all([
      usersApi.list({ search: search.value.trim(), syncStatus: syncStatus.value, status: usageStatus.value }),
      serversApi.list(),
    ])
    users.value = userItems
    servers.value = serverItems
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '代理用户列表加载失败'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function targetSummary(user: ProxyUser): string {
  const ids = user.serverIds || []
  if (!ids.length) return `${user.serverCount || 0} 台服务器`
  const names = ids.map((id) => serverMap.value.get(id)).filter(Boolean) as string[]
  if (!names.length) return `${user.serverCount || ids.length} 台服务器`
  if (names.length <= 2) return names.join('、')
  return `${names.slice(0, 2).join('、')} 等 ${names.length} 台`
}

function usagePercent(user: ProxyUser): number {
  if (!user.trafficLimitBytes) return 0
  return (user.trafficUsedBytes / user.trafficLimitBytes) * 100
}

function usageTone(user: ProxyUser): 'default' | 'warning' | 'danger' {
  const percent = usagePercent(user)
  if (percent >= 100) return 'danger'
  if (percent >= 80) return 'warning'
  return 'default'
}

function usageTitle(user: ProxyUser): string {
  const parts = [`本周期已用 ${formatBytes(user.trafficUsedBytes)}`]
  if (user.periodStartedAt) parts.push(`周期开始 ${formatFullDateTime(user.periodStartedAt)}`)
  if (user.trafficUpdatedAt) parts.push(`采集于 ${formatFullDateTime(user.trafficUpdatedAt)}`)
  if (user.resetPeriod !== 'none') parts.push(describeResetRule(user.resetPeriod, user.resetAnchor))
  return parts.join('\n')
}

function expiringSoon(user: ProxyUser): boolean {
  if (!user.expiresAt) return false
  const remaining = new Date(user.expiresAt).getTime() - Date.now()
  return remaining > 0 && remaining < 3 * 24 * 3_600_000
}

function openCreate(): void {
  editingUser.value = null
  formOpen.value = true
}

function openEdit(user: ProxyUser): void {
  editingUser.value = user
  formOpen.value = true
}

function jobDetail(result: { job?: { id: string } }, fallback?: string): string | undefined {
  return result.job ? `同步任务 #${compactId(result.job.id)} 已创建` : fallback
}

async function submitUser(input: ProxyUserInput): Promise<void> {
  formSubmitting.value = true
  formFieldErrors.value = undefined
  try {
    const result = editingUser.value
      ? await usersApi.update(editingUser.value.id, input)
      : await usersApi.create(input)
    const title = editingUser.value ? '代理用户已更新' : '代理用户已创建'
    toast.notify('success', title, jobDetail(result))
    formOpen.value = false
    if (result.generatedPassword) {
      secretRecord.value = { username: result.user?.username || input.username, password: result.generatedPassword }
    }
    await load(true)
  } catch (caught) {
    if (caught instanceof ApiError && caught.fields) formFieldErrors.value = caught.fields
    toast.notify('error', '保存失败', caught instanceof Error ? caught.message : '请检查输入后重试')
  } finally {
    formSubmitting.value = false
  }
}

async function removeUser(): Promise<void> {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    const result = await usersApi.remove(deleteTarget.value.id)
    toast.notify('success', '删除任务已创建', result.job ? `任务 #${compactId(result.job.id)} 将从目标服务器移除该账号` : deleteTarget.value.username)
    deleteTarget.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', '删除失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    deleting.value = false
  }
}

async function resetTraffic(): Promise<void> {
  if (!resetTarget.value) return
  resetting.value = true
  try {
    const result = await usersApi.resetTraffic(resetTarget.value.id)
    toast.notify('success', '流量已重置', jobDetail(result, resetTarget.value.username))
    resetTarget.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', '重置失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    resetting.value = false
  }
}

async function toggleState(): Promise<void> {
  const user = stateTarget.value
  if (!user) return
  changingState.value = true
  try {
    const result = await usersApi.setEnabled(user.id, !user.enabled)
    toast.notify('success', user.enabled ? '代理用户已停用' : '代理用户已启用', jobDetail(result, user.username))
    stateTarget.value = null
    await load(true)
  } catch (caught) {
    toast.notify('error', user.enabled ? '停用失败' : '启用失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    changingState.value = false
  }
}

watch(search, () => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => void load(), 250)
})
watch([syncStatus, usageStatus], () => void load())
onMounted(() => {
  void load()
  refreshTimer = window.setInterval(() => void load(true), 10_000)
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
        <h2>代理用户</h2>
      </div>
      <button class="button primary" type="button" @click="openCreate"><CirclePlus :size="17" />创建用户</button>
    </header>

    <div class="inline-summary" aria-label="代理用户摘要">
      <span><UsersRound :size="15" /><strong>{{ summary.total }}</strong> 个代理用户</span>
      <span><i class="summary-dot success" />{{ summary.synced }} 个已同步</span>
      <span><i class="summary-dot warning" />{{ summary.attention }} 个需要处理</span>
      <span><i class="summary-dot danger" />{{ summary.limited }} 个到期或流量用尽</span>
      <span v-if="summary.disabled"><i class="summary-dot" />{{ summary.disabled }} 个已停用</span>
    </div>

    <section class="surface-panel table-panel">
      <div class="table-toolbar">
        <div class="search-control">
          <Search :size="17" />
          <input v-model="search" type="search" placeholder="搜索代理用户名" aria-label="搜索代理用户" />
        </div>
        <div class="toolbar-actions">
          <select v-model="usageStatus" class="filter-select" aria-label="使用状态筛选">
            <option value="">全部使用状态</option>
            <option value="active">正常</option>
            <option value="exhausted">流量用尽</option>
            <option value="expired">已到期</option>
            <option value="disabled">已停用</option>
          </select>
          <select v-model="syncStatus" class="filter-select" aria-label="同步状态筛选">
            <option value="">全部同步状态</option>
            <option value="synced">已同步</option>
            <option value="pending">等待同步</option>
            <option value="partial">部分同步</option>
            <option value="failed">同步失败</option>
          </select>
          <button class="icon-button bordered" type="button" aria-label="刷新代理用户列表" title="刷新" :disabled="refreshing" @click="load(true)">
            <RefreshCw :size="17" :class="{ spinning: refreshing }" />
          </button>
        </div>
      </div>

      <div v-if="error" class="inline-error">
        <AlertCircle :size="17" /><span>{{ error }}</span><button type="button" @click="load()">重试</button>
      </div>
      <TableSkeleton v-if="loading" :rows="6" />
      <div v-else-if="users.length" class="table-scroll">
        <table class="data-table users-table">
          <thead>
            <tr>
              <th>代理用户</th>
              <th>状态</th>
              <th>本周期流量</th>
              <th class="hide-mobile">到期时间</th>
              <th class="hide-tablet">目标服务器</th>
              <th class="hide-tablet">最后变更</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="user in users" :key="user.id">
              <td>
                <div class="user-identity-cell">
                  <span class="user-icon"><KeyRound :size="17" /></span>
                  <div class="primary-cell" :title="`创建于 ${formatFullDateTime(user.createdAt)}`">
                    <strong class="mono username">{{ user.username }}</strong><small class="mono">{{ user.id }}</small>
                  </div>
                </div>
              </td>
              <td>
                <div class="status-stack">
                  <StatusBadge :status="user.status" />
                  <StatusBadge :status="user.syncStatus" compact />
                </div>
              </td>
              <td>
                <div class="usage-cell" :title="usageTitle(user)">
                  <span class="usage-numbers">
                    {{ formatBytes(user.trafficUsedBytes) }} <em>/ {{ user.trafficLimitBytes ? formatBytes(user.trafficLimitBytes) : '不限' }}</em>
                  </span>
                  <ProgressBar v-if="user.trafficLimitBytes" :value="usagePercent(user)" :tone="usageTone(user)" />
                  <small v-if="user.resetPeriod !== 'none' && user.nextResetAt">{{ formatDateTime(user.nextResetAt) }} 重置</small>
                </div>
              </td>
              <td class="hide-mobile">
                <div v-if="user.expiresAt" class="expiry-cell" :title="formatFullDateTime(user.expiresAt)">
                  <span>{{ formatDateTime(user.expiresAt) }}</span>
                  <small :class="{ expiring: expiringSoon(user) }">{{ describeRemaining(user.expiresAt) }}</small>
                </div>
                <span v-else class="muted-cell">永久</span>
              </td>
              <td class="hide-tablet">
                <div class="target-cell"><ServerIcon :size="15" /><span :title="user.serverIds?.map((id) => serverMap.get(id) || id).join('、')">{{ targetSummary(user) }}</span></div>
              </td>
              <td class="hide-tablet"><span :title="formatDateTime(user.updatedAt)">{{ formatRelativeTime(user.updatedAt) }}</span></td>
              <td>
                <div class="row-actions">
                  <button class="icon-button" type="button" aria-label="编辑代理用户" title="编辑用户" @click="openEdit(user)"><Pencil :size="16" /></button>
                  <button class="icon-button" type="button" aria-label="重置本周期流量" title="重置流量" @click="resetTarget = user"><RotateCcw :size="16" /></button>
                  <button
                    class="icon-button"
                    type="button"
                    :aria-label="user.enabled ? '停用代理用户' : '启用代理用户'"
                    :title="user.enabled ? '停用用户' : '启用用户'"
                    @click="stateTarget = user"
                  >
                    <CirclePause v-if="user.enabled" :size="16" /><CirclePlay v-else :size="16" />
                  </button>
                  <button class="icon-button danger-icon" type="button" aria-label="删除代理用户" title="删除代理用户" @click="deleteTarget = user"><Trash2 :size="16" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else :title="hasFilters ? '没有匹配的代理用户' : '尚未创建代理用户'" :description="hasFilters ? '调整搜索关键词或筛选条件后重试。' : '创建代理用户并选择需要同步的服务器。'">
        <button v-if="!hasFilters" class="button primary compact" type="button" @click="openCreate">创建用户</button>
      </EmptyState>
    </section>

    <UserFormDialog :open="formOpen" :user="editingUser" :servers="servers" :submitting="formSubmitting" :field-errors="formFieldErrors" @close="formOpen = false" @submit="submitUser" />
    <SecretRevealDialog
      :open="Boolean(secretRecord)"
      :username="secretRecord?.username || ''"
      :password="secretRecord?.password || ''"
      @close="secretRecord = null"
    />
    <ConfirmDialog
      :open="Boolean(resetTarget)"
      title="重置流量"
      :description="`重置代理用户 ${resetTarget?.username || ''} 的本周期流量？`"
      :detail="`已用流量将从 0 重新计算${resetTarget?.status === 'exhausted' ? '，因流量用尽而停用的账号会自动恢复' : ''}。周期重置时间不变。`"
      confirm-label="重置并同步"
      :busy="resetting"
      @close="resetTarget = null"
      @confirm="resetTraffic"
    />
    <ConfirmDialog
      :open="Boolean(stateTarget)"
      :title="stateTarget?.enabled ? '停用代理用户' : '启用代理用户'"
      :description="`${stateTarget?.enabled ? '停用' : '启用'}代理用户 ${stateTarget?.username || ''}？`"
      :detail="stateTarget?.enabled ? '停用后该账号无法认证，已建立的连接会被断开。账号与流量记录会保留。' : '启用后，如账号未到期且流量未用尽，即可恢复使用。'"
      :confirm-label="stateTarget?.enabled ? '停用并同步' : '启用并同步'"
      :danger="stateTarget?.enabled"
      :busy="changingState"
      @close="stateTarget = null"
      @confirm="toggleState"
    />
    <ConfirmDialog
      :open="Boolean(deleteTarget)"
      title="删除代理用户"
      :description="`删除代理用户 ${deleteTarget?.username || ''}？`"
      :detail="`将从 ${deleteTarget?.serverCount || deleteTarget?.serverIds?.length || 0} 台目标服务器移除该凭据。失败节点会保留在任务记录中以便重试。`"
      confirm-label="删除并同步"
      danger
      :busy="deleting"
      @close="deleteTarget = null"
      @confirm="removeUser"
    />
  </div>
</template>
