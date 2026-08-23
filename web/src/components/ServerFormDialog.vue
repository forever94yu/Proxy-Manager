<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { KeyRound, LockKeyhole } from 'lucide-vue-next'

import ModalDialog from '@/components/ModalDialog.vue'
import type { AuthMethod, Server, ServerInput } from '@/types'

const props = defineProps<{
  open: boolean
  server?: Server | null
  submitting?: boolean
}>()

const emit = defineEmits<{
  close: []
  submit: [input: ServerInput]
}>()

interface ServerForm {
  name: string
  host: string
  sshPort: number
  sshUser: string
  authMethod: AuthMethod
  credential: string
  httpPort: number
  socksPort: number
  dns: string
  tags: string
}

const form = reactive<ServerForm>({
  name: '',
  host: '',
  sshPort: 22,
  sshUser: 'proxymgr',
  authMethod: 'key',
  credential: '',
  httpPort: 3128,
  socksPort: 1080,
  dns: '1.1.1.1, 1.0.0.1',
  tags: '',
})
const errors = ref<Record<string, string>>({})

const isEditing = computed(() => Boolean(props.server))
const originalAuthMethod = computed<AuthMethod>(() => props.server?.authMethod || 'key')
const credentialRequired = computed(() => !isEditing.value || form.authMethod !== originalAuthMethod.value)

watch([() => props.open, () => props.server], ([open, server]) => {
  if (!open) return
  Object.assign(form, {
    name: server?.name || '',
    host: server?.host || '',
    sshPort: server?.sshPort || 22,
    sshUser: server?.sshUser || 'proxymgr',
    authMethod: server?.authMethod || 'key' as AuthMethod,
    credential: '',
    httpPort: server?.httpPort || 3128,
    socksPort: server?.socksPort || 1080,
    dns: server?.dns?.join(', ') || '1.1.1.1, 1.0.0.1',
    tags: server?.tags?.join(', ') || '',
  })
  errors.value = {}
}, { immediate: true })

function validate(): boolean {
  const next: Record<string, string> = {}
  const dnsServers = form.dns.split(',').map((item) => item.trim()).filter(Boolean)
  const ipv4Pattern = /^(?:(?:25[0-5]|2[0-4]\d|1\d{2}|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d{2}|[1-9]?\d)$/
  if (!form.name.trim()) next.name = '请输入服务器名称'
  if (form.name.trim().length > 64) next.name = '名称不能超过 64 个字符'
  if (!form.host.trim() || /\s/.test(form.host)) next.host = '请输入有效的主机名或 IP 地址'
  if (!Number.isInteger(Number(form.sshPort)) || form.sshPort < 1 || form.sshPort > 65535) next.sshPort = 'SSH 端口范围为 1–65535'
  if (!/^[a-zA-Z_][a-zA-Z0-9_-]*$/.test(form.sshUser)) next.sshUser = '请输入有效的 SSH 用户名'
  if (credentialRequired.value && !form.credential.trim()) {
    if (!isEditing.value) {
      next.credential = form.authMethod === 'key' ? '请粘贴 SSH 私钥' : '请输入 SSH 密码'
    } else {
      next.credential = form.authMethod === 'key'
        ? '切换为密钥认证时，请粘贴新的 SSH 私钥'
        : '切换为密码认证时，请输入新的 SSH 密码'
    }
  }
  if (!Number.isInteger(Number(form.httpPort)) || form.httpPort < 1 || form.httpPort > 65535) next.httpPort = 'HTTP 端口范围为 1–65535'
  if (!Number.isInteger(Number(form.socksPort)) || form.socksPort < 1 || form.socksPort > 65535) next.socksPort = 'SOCKS 端口范围为 1–65535'
  if (Number(form.httpPort) === Number(form.socksPort)) next.socksPort = 'SOCKS 端口不能与 HTTP 端口相同'
  if (dnsServers.length < 1 || dnsServers.length > 2 || dnsServers.some((item) => !ipv4Pattern.test(item))) {
    next.dns = '请输入 1–2 个合法 IPv4 地址，以逗号分隔'
  }
  errors.value = next
  return Object.keys(next).length === 0
}

function submit(): void {
  if (!validate()) return
  const input: ServerInput = {
    name: form.name.trim(),
    host: form.host.trim(),
    sshPort: Number(form.sshPort),
    sshUser: form.sshUser.trim(),
    authMethod: form.authMethod,
    httpPort: Number(form.httpPort),
    socksPort: Number(form.socksPort),
    dns: form.dns.split(',').map((item) => item.trim()).filter(Boolean),
    tags: form.tags.split(',').map((item) => item.trim()).filter(Boolean),
  }
  if (form.credential.trim()) input.credential = form.credential.trim()
  emit('submit', input)
}

function setAuthMethod(method: AuthMethod): void {
  if (form.authMethod === method) return
  form.authMethod = method
  form.credential = ''
  delete errors.value.credential
}
</script>

<template>
  <ModalDialog
    :open="open"
    :title="isEditing ? '编辑服务器' : '添加服务器'"
    :description="isEditing ? '更新连接信息和代理服务配置。' : '登记 SSH 连接信息，保存后可测试连接并部署。'"
    size="large"
    :busy="submitting"
    @close="$emit('close')"
  >
    <form id="server-form" class="form-stack" novalidate @submit.prevent="submit">
      <section class="form-section">
        <div class="form-section-heading">
          <h3>基本信息</h3>
          <p>用于在服务器列表中识别和筛选节点。</p>
        </div>
        <div class="form-grid two-columns">
          <label class="field full-mobile">
            <span>服务器名称</span>
            <input v-model="form.name" :class="{ invalid: errors.name }" autocomplete="off" placeholder="例如：上海出口 01" />
            <small v-if="errors.name" class="field-error">{{ errors.name }}</small>
          </label>
          <label class="field full-mobile">
            <span>标签 <em>可选</em></span>
            <input v-model="form.tags" autocomplete="off" placeholder="生产, 华东" />
          </label>
        </div>
      </section>

      <section class="form-section">
        <div class="form-section-heading">
          <h3>SSH 连接</h3>
          <p>凭据只会提交给管理服务，不会在保存后回显。</p>
        </div>
        <div class="form-grid host-grid">
          <label class="field host-field">
            <span>主机地址</span>
            <input v-model="form.host" :class="{ invalid: errors.host }" autocomplete="off" spellcheck="false" placeholder="IP 地址或主机名" />
            <small v-if="errors.host" class="field-error">{{ errors.host }}</small>
          </label>
          <label class="field port-field">
            <span>端口</span>
            <input v-model.number="form.sshPort" :class="{ invalid: errors.sshPort }" type="number" inputmode="numeric" min="1" max="65535" />
            <small v-if="errors.sshPort" class="field-error">{{ errors.sshPort }}</small>
          </label>
          <label class="field user-field">
            <span>SSH 用户</span>
            <input v-model="form.sshUser" :class="{ invalid: errors.sshUser }" autocomplete="off" spellcheck="false" />
            <small v-if="errors.sshUser" class="field-error">{{ errors.sshUser }}</small>
          </label>
        </div>

        <div class="field">
          <span>认证方式</span>
          <div class="segmented-control" role="radiogroup" aria-label="SSH 认证方式">
            <button type="button" :class="{ active: form.authMethod === 'key' }" @click="setAuthMethod('key')">
              <KeyRound :size="16" /> 密钥
            </button>
            <button type="button" :class="{ active: form.authMethod === 'password' }" @click="setAuthMethod('password')">
              <LockKeyhole :size="16" /> 密码
            </button>
          </div>
        </div>

        <label class="field">
          <span>
            {{ form.authMethod === 'key' ? 'SSH 私钥' : 'SSH 密码' }}
            <em v-if="isEditing && !credentialRequired">留空则保持不变</em>
            <em v-else-if="isEditing">切换认证方式需重新填写</em>
          </span>
          <textarea
            v-if="form.authMethod === 'key'"
            v-model="form.credential"
            :class="{ invalid: errors.credential }"
            rows="4"
            spellcheck="false"
            autocomplete="off"
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          />
          <input
            v-else
            v-model="form.credential"
            :class="{ invalid: errors.credential }"
            type="password"
            autocomplete="new-password"
            placeholder="输入 SSH 密码"
          />
          <small v-if="errors.credential" class="field-error">{{ errors.credential }}</small>
        </label>
      </section>

      <section class="form-section">
        <div class="form-section-heading">
          <h3>代理服务</h3>
          <p>部署时写入目标服务器的 3proxy 配置。</p>
        </div>
        <div class="form-grid two-columns">
          <label class="field">
            <span>HTTP / HTTPS 端口</span>
            <input v-model.number="form.httpPort" :class="{ invalid: errors.httpPort }" type="number" inputmode="numeric" min="1" max="65535" />
            <small v-if="errors.httpPort" class="field-error">{{ errors.httpPort }}</small>
          </label>
          <label class="field">
            <span>SOCKS5 端口</span>
            <input v-model.number="form.socksPort" :class="{ invalid: errors.socksPort }" type="number" inputmode="numeric" min="1" max="65535" />
            <small v-if="errors.socksPort" class="field-error">{{ errors.socksPort }}</small>
          </label>
          <label class="field full-span">
            <span>DNS 服务器</span>
            <input v-model="form.dns" :class="{ invalid: errors.dns }" spellcheck="false" placeholder="1.1.1.1, 1.0.0.1" />
            <small v-if="errors.dns" class="field-error">{{ errors.dns }}</small>
          </label>
        </div>
      </section>
    </form>

    <template #footer>
      <button class="button secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button class="button primary" type="submit" form="server-form" :disabled="submitting">
        <span v-if="submitting" class="button-spinner" />
        {{ submitting ? '保存中' : isEditing ? '保存修改' : '添加服务器' }}
      </button>
    </template>
  </ModalDialog>
</template>
