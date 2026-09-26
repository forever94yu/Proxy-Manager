<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { CalendarClock, Gauge, Infinity as InfinityIcon, KeyRound, PencilLine, RotateCw } from 'lucide-vue-next'

import ModalDialog from '@/components/ModalDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import type { PasswordMode, ProxyUser, ProxyUserInput, ResetPeriod, Server } from '@/types'
import { describeResetRule, formatBytes, formatFullDateTime, fromLocalInputValue, toLocalInputValue } from '@/utils'

const props = defineProps<{
  open: boolean
  user?: ProxyUser | null
  servers: Server[]
  submitting?: boolean
  /** Field errors returned by the API (already translated). */
  fieldErrors?: Record<string, string>
}>()

const emit = defineEmits<{
  close: []
  submit: [input: ProxyUserInput]
}>()

type QuotaUnit = 'MB' | 'GB' | 'TB'
const UNIT_BYTES: Record<QuotaUnit, number> = { MB: 1024 ** 2, GB: 1024 ** 3, TB: 1024 ** 4 }
const MAX_LIMIT_BYTES = 1024 ** 5
const EXPIRY_PRESETS = [
  { label: '+7 天', days: 7 },
  { label: '+30 天', days: 30 },
  { label: '+90 天', days: 90 },
  { label: '+1 年', days: 365 },
]
const RESET_OPTIONS: { value: ResetPeriod; label: string }[] = [
  { value: 'none', label: '不重置' },
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周' },
  { value: 'monthly', label: '每月' },
]

const form = reactive({
  username: '',
  passwordMode: 'generated' as PasswordMode,
  password: '',
  serverIds: [] as string[],
  enabled: true,
  quotaMode: 'unlimited' as 'unlimited' | 'limited',
  quotaValue: '',
  quotaUnit: 'GB' as QuotaUnit,
  expiryMode: 'never' as 'never' | 'date',
  expiresAt: '',
  resetPeriod: 'none' as ResetPeriod,
  resetAnchor: '',
})
const errors = ref<Record<string, string>>({})

const isEditing = computed(() => Boolean(props.user))
const selectableServers = computed(() => props.servers.filter((server) => server.installStatus === 'installed'))
const allSelected = computed(() => selectableServers.value.length > 0 && selectableServers.value.every((server) => form.serverIds.includes(server.id)))
const expiryInPast = computed(() => form.expiryMode === 'date' && Boolean(form.expiresAt) && new Date(form.expiresAt).getTime() <= Date.now())
const resetRule = computed(() => describeResetRule(form.resetPeriod, fromLocalInputValue(form.resetAnchor)))

/** Chooses the largest unit that represents the limit as a whole number. */
function splitLimit(bytes: number): { value: string; unit: QuotaUnit } {
  for (const unit of ['TB', 'GB'] as QuotaUnit[]) {
    if (bytes % UNIT_BYTES[unit] === 0) return { value: String(bytes / UNIT_BYTES[unit]), unit }
  }
  const value = bytes / UNIT_BYTES.MB
  return { value: String(Number.isInteger(value) ? value : Number(value.toFixed(2))), unit: 'MB' }
}

watch([() => props.open, () => props.user], ([open, user]) => {
  if (!open) return
  const limit = user?.trafficLimitBytes || 0
  const split = limit > 0 ? splitLimit(limit) : { value: '', unit: 'GB' as QuotaUnit }
  Object.assign(form, {
    username: user?.username || '',
    passwordMode: user ? 'unchanged' as PasswordMode : 'generated' as PasswordMode,
    password: '',
    serverIds: [...(user?.serverIds || [])],
    enabled: user ? user.enabled : true,
    quotaMode: limit > 0 ? 'limited' : 'unlimited',
    quotaValue: split.value,
    quotaUnit: split.unit,
    expiryMode: user?.expiresAt ? 'date' : 'never',
    expiresAt: toLocalInputValue(user?.expiresAt),
    resetPeriod: user?.resetPeriod || 'none',
    resetAnchor: toLocalInputValue(user?.resetAnchor) || toLocalInputValue(new Date().toISOString()),
  })
  errors.value = {}
}, { immediate: true })

watch(() => props.fieldErrors, (fields) => {
  if (!fields || !Object.keys(fields).length) return
  const next = { ...fields }
  if (next.passwordMode && !next.password) next.password = next.passwordMode
  errors.value = next
})

function toggleAll(): void {
  // Keep servers that are already bound but not currently selectable (e.g. an
  // installation that is failed/in progress); otherwise "select all / clear"
  // would silently remove the user from those servers on save.
  const selectableIds = selectableServers.value.map((server) => server.id)
  const locked = form.serverIds.filter((id) => !selectableIds.includes(id))
  form.serverIds = allSelected.value ? locked : [...locked, ...selectableIds]
}

/** Extends the expiry from the later of now and the current value. */
function extendExpiry(days: number): void {
  const current = form.expiryMode === 'date' && form.expiresAt ? new Date(form.expiresAt).getTime() : 0
  const base = new Date(Math.max(Date.now(), Number.isNaN(current) ? 0 : current))
  base.setDate(base.getDate() + days)
  form.expiryMode = 'date'
  form.expiresAt = toLocalInputValue(base.toISOString())
}

function limitBytes(): number {
  if (form.quotaMode === 'unlimited') return 0
  return Math.round(Number(form.quotaValue) * UNIT_BYTES[form.quotaUnit])
}

function validate(): boolean {
  const next: Record<string, string> = {}
  if (!/^[a-zA-Z0-9_-]+$/.test(form.username)) next.username = '仅支持字母、数字、下划线和短横线'
  if (form.username.length > 64) next.username = '用户名不能超过 64 个字符'
  if (form.passwordMode === 'custom') {
    if (!/^[A-Za-z0-9_@%+=,.!?-]{8,128}$/.test(form.password)) {
      next.password = '请输入 8–128 位字母、数字或 _@%+=,.!?-'
    }
  }
  if (!form.serverIds.length) next.serverIds = '至少选择一台已部署服务器'
  if (form.quotaMode === 'limited') {
    const value = Number(form.quotaValue)
    const bytes = limitBytes()
    if (!form.quotaValue.trim() || !Number.isFinite(value) || value <= 0) next.trafficLimitBytes = '请输入大于 0 的流量额度'
    else if (bytes < UNIT_BYTES.MB) next.trafficLimitBytes = '流量额度不能小于 1 MB'
    else if (bytes > MAX_LIMIT_BYTES) next.trafficLimitBytes = '流量额度不能超过 1 PB'
  }
  if (form.expiryMode === 'date' && !fromLocalInputValue(form.expiresAt)) next.expiresAt = '请选择到期时间'
  if (form.resetPeriod !== 'none' && !fromLocalInputValue(form.resetAnchor)) next.resetAnchor = '请选择周期起点'
  errors.value = next
  return Object.keys(next).length === 0
}

function submit(): void {
  if (!validate()) return
  const input: ProxyUserInput = {
    username: form.username.trim(),
    passwordMode: form.passwordMode,
    serverIds: [...form.serverIds],
    enabled: form.enabled,
    trafficLimitBytes: limitBytes(),
    expiresAt: form.expiryMode === 'date' ? fromLocalInputValue(form.expiresAt) : '',
    resetPeriod: form.resetPeriod,
    resetAnchor: form.resetPeriod === 'none' ? '' : fromLocalInputValue(form.resetAnchor),
  }
  if (form.passwordMode === 'custom') input.password = form.password
  emit('submit', input)
}
</script>

<template>
  <ModalDialog
    :open="open"
    :title="isEditing ? '编辑代理用户' : '创建代理用户'"
    :description="isEditing ? '调整凭据、使用限制和目标服务器，变更将作为同步任务执行。' : '创建凭据并分发到一台或多台服务器。'"
    size="large"
    :busy="submitting"
    @close="$emit('close')"
  >
    <form id="user-form" class="form-stack" novalidate @submit.prevent="submit">
      <section class="form-section">
        <div class="form-section-heading">
          <h3>账号凭据</h3>
          <p>密码保存后不会再次回显；生成密码仅展示一次。</p>
        </div>
        <label class="field">
          <span>代理用户名</span>
          <input v-model="form.username" :class="{ invalid: errors.username }" autocomplete="off" spellcheck="false" placeholder="例如：crawler_cn" />
          <small v-if="errors.username" class="field-error">{{ errors.username }}</small>
        </label>

        <div class="field">
          <span>密码处理</span>
          <div class="segmented-control password-mode" role="radiogroup" aria-label="密码处理方式">
            <button v-if="isEditing" type="button" :class="{ active: form.passwordMode === 'unchanged' }" @click="form.passwordMode = 'unchanged'">
              <PencilLine :size="16" /> 保持不变
            </button>
            <button type="button" :class="{ active: form.passwordMode === 'generated' }" @click="form.passwordMode = 'generated'">
              <RotateCw :size="16" /> 自动生成
            </button>
            <button type="button" :class="{ active: form.passwordMode === 'custom' }" @click="form.passwordMode = 'custom'">
              <KeyRound :size="16" /> 自定义
            </button>
          </div>
        </div>

        <label v-if="form.passwordMode === 'custom'" class="field">
          <span>自定义密码</span>
          <input v-model="form.password" :class="{ invalid: errors.password }" type="password" autocomplete="new-password" placeholder="8–128 位安全字符" />
          <small v-if="errors.password" class="field-error">{{ errors.password }}</small>
        </label>
      </section>

      <section class="form-section">
        <div class="form-section-heading inline-heading">
          <div>
            <h3>使用限制</h3>
            <p>流量额度按全部目标服务器合计，统计上行与下行。超额或到期后账号自动停用并断开连接。</p>
          </div>
          <label class="switch-control">
            <input v-model="form.enabled" type="checkbox" role="switch" :aria-checked="form.enabled" />
            <span class="switch-track" aria-hidden="true"><span class="switch-thumb" /></span>
            <span>{{ form.enabled ? '账号已启用' : '账号已停用' }}</span>
          </label>
        </div>

        <div class="field">
          <span>流量额度<em v-if="isEditing && user">本周期已用 {{ formatBytes(user.trafficUsedBytes) }}</em></span>
          <div class="limit-row">
            <div class="segmented-control" role="radiogroup" aria-label="流量额度">
              <button type="button" :class="{ active: form.quotaMode === 'unlimited' }" @click="form.quotaMode = 'unlimited'">
                <InfinityIcon :size="16" /> 不限
              </button>
              <button type="button" :class="{ active: form.quotaMode === 'limited' }" @click="form.quotaMode = 'limited'">
                <Gauge :size="16" /> 限额
              </button>
            </div>
            <div v-if="form.quotaMode === 'limited'" class="unit-input" :class="{ invalid: errors.trafficLimitBytes }">
              <input v-model="form.quotaValue" type="number" min="0" step="any" inputmode="decimal" placeholder="例如 100" aria-label="流量额度数值" />
              <select v-model="form.quotaUnit" aria-label="流量额度单位">
                <option value="MB">MB</option>
                <option value="GB">GB</option>
                <option value="TB">TB</option>
              </select>
            </div>
          </div>
          <small v-if="errors.trafficLimitBytes" class="field-error">{{ errors.trafficLimitBytes }}</small>
        </div>

        <div class="field">
          <span>使用期限</span>
          <div class="limit-row">
            <div class="segmented-control" role="radiogroup" aria-label="使用期限">
              <button type="button" :class="{ active: form.expiryMode === 'never' }" @click="form.expiryMode = 'never'">
                <InfinityIcon :size="16" /> 永久
              </button>
              <button type="button" :class="{ active: form.expiryMode === 'date' }" @click="form.expiryMode = 'date'">
                <CalendarClock :size="16" /> 指定到期
              </button>
            </div>
            <input v-if="form.expiryMode === 'date'" v-model="form.expiresAt" class="datetime-input" :class="{ invalid: errors.expiresAt }" type="datetime-local" aria-label="到期时间" />
          </div>
          <div class="preset-row" aria-label="快速续期">
            <button v-for="preset in EXPIRY_PRESETS" :key="preset.days" class="chip-button" type="button" @click="extendExpiry(preset.days)">{{ preset.label }}</button>
          </div>
          <small v-if="errors.expiresAt" class="field-error">{{ errors.expiresAt }}</small>
          <small v-else-if="expiryInPast" class="field-warning">到期时间已过，保存后账号将立即停用。</small>
          <small v-else-if="isEditing && user?.expiresAt && form.expiryMode === 'date'" class="field-hint">当前到期：{{ formatFullDateTime(user.expiresAt) }}</small>
        </div>

        <div class="field">
          <span>流量重置</span>
          <div class="limit-row">
            <div class="segmented-control" role="radiogroup" aria-label="流量重置周期">
              <button v-for="option in RESET_OPTIONS" :key="option.value" type="button" :class="{ active: form.resetPeriod === option.value }" @click="form.resetPeriod = option.value">
                {{ option.label }}
              </button>
            </div>
            <input v-if="form.resetPeriod !== 'none'" v-model="form.resetAnchor" class="datetime-input" :class="{ invalid: errors.resetAnchor }" type="datetime-local" aria-label="周期起点" title="周期起点" />
          </div>
          <small v-if="errors.resetAnchor || errors.resetPeriod" class="field-error">{{ errors.resetAnchor || errors.resetPeriod }}</small>
          <small v-else-if="form.resetPeriod !== 'none'" class="field-hint">{{ resetRule }}。修改额度或周期不会清空本周期已用流量。</small>
        </div>
      </section>

      <section class="form-section">
        <div class="form-section-heading inline-heading">
          <div>
            <h3>目标服务器</h3>
            <p>同步任务会分别记录每台服务器的结果。</p>
          </div>
          <button class="text-button" type="button" @click="toggleAll">{{ allSelected ? '取消全选' : '选择全部' }}</button>
        </div>
        <div class="server-picker" :class="{ invalid: errors.serverIds }">
          <label v-for="server in servers" :key="server.id" class="server-option" :class="{ disabled: server.installStatus !== 'installed' }">
            <input v-model="form.serverIds" type="checkbox" :value="server.id" :disabled="server.installStatus !== 'installed'" />
            <span class="checkbox-mark" />
            <span class="server-option-copy">
              <strong>{{ server.name }}</strong>
              <small>{{ server.host }} · {{ server.httpPort || '—' }}/{{ server.socksPort || '—' }}</small>
            </span>
            <StatusBadge :status="server.installStatus" compact />
          </label>
          <div v-if="!servers.length" class="picker-empty">暂无服务器，请先添加并部署服务器。</div>
        </div>
        <small v-if="errors.serverIds" class="field-error">{{ errors.serverIds }}</small>
        <p class="selection-summary">已选择 {{ form.serverIds.length }} 台服务器</p>
      </section>
    </form>

    <template #footer>
      <button class="button secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button class="button primary" type="submit" form="user-form" :disabled="submitting">
        <span v-if="submitting" class="button-spinner" />
        {{ submitting ? '提交中' : isEditing ? '保存并同步' : '创建并同步' }}
      </button>
    </template>
  </ModalDialog>
</template>
