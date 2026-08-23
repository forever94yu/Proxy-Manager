<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AlertCircle,
  CirclePlus,
  KeyRound,
  Pencil,
  RefreshCw,
  Search,
  Server as ServerIcon,
  Trash2,
  UsersRound,
} from 'lucide-vue-next'

import { serversApi, usersApi } from '@/api'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import SecretRevealDialog from '@/components/SecretRevealDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import UserFormDialog from '@/components/UserFormDialog.vue'
import { useToastStore } from '@/stores/toast'
import type { ProxyUser, ProxyUserInput, Server } from '@/types'
import { compactId, formatDateTime, formatRelativeTime } from '@/utils'

const toast = useToastStore()
const users = ref<ProxyUser[]>([])
const servers = ref<Server[]>([])
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const search = ref('')
const syncStatus = ref('')
const formOpen = ref(false)
const editingUser = ref<ProxyUser | null>(null)
const formSubmitting = ref(false)
const deleteTarget = ref<ProxyUser | null>(null)
const deleting = ref(false)
const secretRecord = ref<{ username: string; password: string } | null>(null)
let searchTimer: number | undefined
let refreshTimer: number | undefined

const summary = computed(() => ({
  total: users.value.length,
  synced: users.value.filter((user) => user.syncStatus === 'synced').length,
  attention: users.value.filter((user) => user.syncStatus === 'failed' || user.syncStatus === 'partial').length,
}))

const serverMap = computed(() => new Map(servers.value.map((server) => [server.id, server.name])))

async function load(silent = false): Promise<void> {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    const [userItems, serverItems] = await Promise.all([
      usersApi.list({ search: search.value.trim(), syncStatus: syncStatus.value }),
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

function openCreate(): void {
  editingUser.value = null
  formOpen.value = true
}

function openEdit(user: ProxyUser): void {
  editingUser.value = user
  formOpen.value = true
}

async function submitUser(input: ProxyUserInput): Promise<void> {
  formSubmitting.value = true
  try {
    const result = editingUser.value
      ? await usersApi.update(editingUser.value.id, input)
      : await usersApi.create(input)
    const title = editingUser.value ? '代理用户已更新' : '代理用户已创建'
    toast.notify('success', title, result.job ? `同步任务 #${compactId(result.job.id)} 已创建` : undefined)
    formOpen.value = false
    if (result.generatedPassword) {
      secretRecord.value = { username: result.user?.username || input.username, password: result.generatedPassword }
    }
    await load(true)
  } catch (caught) {
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

watch(search, () => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => void load(), 250)
})
watch(syncStatus, () => void load())
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
    </div>

    <section class="surface-panel table-panel">
      <div class="table-toolbar">
        <div class="search-control">
          <Search :size="17" />
          <input v-model="search" type="search" placeholder="搜索代理用户名" aria-label="搜索代理用户" />
        </div>
        <div class="toolbar-actions">
          <select v-model="syncStatus" class="filter-select" aria-label="同步状态筛选">
            <option value="">全部状态</option>
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
          <thead><tr><th>代理用户</th><th>同步状态</th><th>目标服务器</th><th class="hide-mobile">最后变更</th><th class="hide-tablet">创建时间</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="user in users" :key="user.id">
              <td>
                <div class="user-identity-cell">
                  <span class="user-icon"><KeyRound :size="17" /></span>
                  <div class="primary-cell"><strong class="mono username">{{ user.username }}</strong><small class="mono">{{ user.id }}</small></div>
                </div>
              </td>
              <td><StatusBadge :status="user.syncStatus" /></td>
              <td>
                <div class="target-cell"><ServerIcon :size="15" /><span :title="user.serverIds?.map((id) => serverMap.get(id) || id).join('、')">{{ targetSummary(user) }}</span></div>
              </td>
              <td class="hide-mobile"><span :title="formatDateTime(user.updatedAt)">{{ formatRelativeTime(user.updatedAt) }}</span></td>
              <td class="hide-tablet">{{ formatDateTime(user.createdAt) }}</td>
              <td>
                <div class="row-actions">
                  <button class="icon-button" type="button" aria-label="编辑代理用户" title="编辑用户" @click="openEdit(user)"><Pencil :size="16" /></button>
                  <button class="icon-button danger-icon" type="button" aria-label="删除代理用户" title="删除代理用户" @click="deleteTarget = user"><Trash2 :size="16" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else :title="search || syncStatus ? '没有匹配的代理用户' : '尚未创建代理用户'" :description="search || syncStatus ? '调整搜索关键词或同步状态后重试。' : '创建代理用户并选择需要同步的服务器。'">
        <button v-if="!search && !syncStatus" class="button primary compact" type="button" @click="openCreate">创建用户</button>
      </EmptyState>
    </section>

    <UserFormDialog :open="formOpen" :user="editingUser" :servers="servers" :submitting="formSubmitting" @close="formOpen = false" @submit="submitUser" />
    <SecretRevealDialog
      :open="Boolean(secretRecord)"
      :username="secretRecord?.username || ''"
      :password="secretRecord?.password || ''"
      @close="secretRecord = null"
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
