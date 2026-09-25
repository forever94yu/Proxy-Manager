<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { KeyRound, PencilLine, RotateCw } from 'lucide-vue-next'

import ModalDialog from '@/components/ModalDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import type { PasswordMode, ProxyUser, ProxyUserInput, Server } from '@/types'

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

const form = reactive({
  username: '',
  passwordMode: 'generated' as PasswordMode,
  password: '',
  serverIds: [] as string[],
})
const errors = ref<Record<string, string>>({})

const isEditing = computed(() => Boolean(props.user))
const selectableServers = computed(() => props.servers.filter((server) => server.installStatus === 'installed'))
const allSelected = computed(() => selectableServers.value.length > 0 && selectableServers.value.every((server) => form.serverIds.includes(server.id)))

watch([() => props.open, () => props.user], ([open, user]) => {
  if (!open) return
  Object.assign(form, {
    username: user?.username || '',
    passwordMode: user ? 'unchanged' as PasswordMode : 'generated' as PasswordMode,
    password: '',
    serverIds: [...(user?.serverIds || [])],
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
  errors.value = next
  return Object.keys(next).length === 0
}

function submit(): void {
  if (!validate()) return
  const input: ProxyUserInput = {
    username: form.username.trim(),
    passwordMode: form.passwordMode,
    serverIds: [...form.serverIds],
  }
  if (form.passwordMode === 'custom') input.password = form.password
  emit('submit', input)
}
</script>

<template>
  <ModalDialog
    :open="open"
    :title="isEditing ? '编辑代理用户' : '创建代理用户'"
    :description="isEditing ? '调整凭据和目标服务器，变更将作为同步任务执行。' : '创建凭据并分发到一台或多台服务器。'"
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
