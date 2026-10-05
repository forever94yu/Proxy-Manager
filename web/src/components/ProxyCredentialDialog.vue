<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { AlertCircle, Check, ClipboardCopy, Server as ServerIcon, ShieldCheck } from 'lucide-vue-next'

import CopyField from '@/components/CopyField.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import ProxyQrImport from '@/components/ProxyQrImport.vue'
import { useToastStore } from '@/stores/toast'
import type { Server } from '@/types'
import { buildProxyUri, copyText, formatHost, formatHostPort } from '@/utils'

export type CredentialDialogMode = 'created' | 'reset' | 'view'

const props = withDefaults(defineProps<{
  open: boolean
  mode: CredentialDialogMode
  /** Needed for the subscription URL; absent if the server did not echo the user. */
  userId?: string
  username: string
  password: string
  /** Servers the user is bound to; one connection block is shown per server. */
  servers: Server[]
  loading?: boolean
  error?: string
}>(), {
  userId: undefined,
  loading: false,
  error: '',
})

defineEmits<{
  close: []
  retry: []
}>()

const toast = useToastStore()
const allCopied = ref(false)
let resetTimer: number | undefined

interface ConnectionEntry {
  server: Server
  host: string
  httpAddress?: string
  socksAddress?: string
  httpUri?: string
  httpUriMasked?: string
  socksUri?: string
  socksUriMasked?: string
}

const connections = computed<ConnectionEntry[]>(() => props.servers.map((server) => {
  const entry: ConnectionEntry = { server, host: formatHost(server.host) }
  if (server.httpPort) {
    entry.httpAddress = formatHostPort(server.host, server.httpPort)
    entry.httpUri = buildProxyUri('http', props.username, props.password, server.host, server.httpPort)
    entry.httpUriMasked = buildProxyUri('http', props.username, props.password, server.host, server.httpPort, true)
  }
  if (server.socksPort) {
    entry.socksAddress = formatHostPort(server.host, server.socksPort)
    entry.socksUri = buildProxyUri('socks5', props.username, props.password, server.host, server.socksPort)
    entry.socksUriMasked = buildProxyUri('socks5', props.username, props.password, server.host, server.socksPort, true)
  }
  return entry
}))

const title = computed(() => {
  if (props.mode === 'created') return '代理用户已创建'
  if (props.mode === 'reset') return '代理密码已重置'
  return '连接信息'
})

const ready = computed(() => !props.loading && !props.error && Boolean(props.password))

/** Plain-text summary of every credential and endpoint, for one-click sharing. */
const summaryText = computed(() => {
  const lines = [`用户名：${props.username}`, `密码：${props.password}`]
  for (const entry of connections.value) {
    lines.push('', `[${entry.server.name}]`, `地址：${entry.host}`)
    if (entry.httpAddress) lines.push(`HTTP 代理：${entry.httpAddress}`)
    if (entry.socksAddress) lines.push(`SOCKS5 代理：${entry.socksAddress}`)
    if (entry.httpUri) lines.push(`HTTP 链接：${entry.httpUri}`)
    if (entry.socksUri) lines.push(`SOCKS5 链接：${entry.socksUri}`)
  }
  return lines.join('\n')
})

async function copyAll(): Promise<void> {
  if (!ready.value) return
  if (await copyText(summaryText.value)) {
    allCopied.value = true
    window.clearTimeout(resetTimer)
    resetTimer = window.setTimeout(() => { allCopied.value = false }, 2_000)
    toast.notify('success', '已复制全部连接信息')
  } else {
    toast.notify('error', '复制失败', '浏览器拒绝访问剪贴板，请手动选择文本复制')
  }
}

watch(() => props.open, (open) => {
  if (open) allCopied.value = false
})

onBeforeUnmount(() => window.clearTimeout(resetTimer))
</script>

<template>
  <ModalDialog
    :open="open"
    :title="title"
    description="用户名、密码、连接地址和导入二维码可随时在代理用户列表的“连接信息”中再次查看。"
    size="medium"
    @close="$emit('close')"
  >
    <div v-if="loading" class="credential-loading" aria-busy="true" aria-label="正在加载连接信息">
      <span class="skeleton-block wide" />
      <span class="skeleton-block" />
      <span class="skeleton-block short" />
    </div>

    <div v-else-if="error" class="inline-error credential-error">
      <AlertCircle :size="17" /><span>{{ error }}</span><button type="button" @click="$emit('retry')">重试</button>
    </div>

    <div v-else class="credential-body">
      <div v-if="mode !== 'view'" class="secret-notice">
        <ShieldCheck :size="20" aria-hidden="true" />
        <p v-if="mode === 'created'">账号 <strong>{{ username }}</strong> 已创建，同步任务正在后台执行。</p>
        <p v-else>账号 <strong>{{ username }}</strong> 的新密码已生成，同步任务正在后台执行。</p>
      </div>

      <ProxyQrImport
        v-if="connections.length"
        :key="userId || username"
        :user-id="userId"
        :username="username"
        :password="password"
        :servers="servers"
      />

      <section class="credential-section">
        <h3>账号凭据</h3>
        <div class="credential-grid">
          <CopyField label="用户名" :value="username" />
          <CopyField label="密码" :value="password" secret />
        </div>
      </section>

      <section v-for="entry in connections" :key="entry.server.id" class="credential-section credential-server">
        <h3><ServerIcon :size="15" aria-hidden="true" />{{ entry.server.name }}</h3>
        <CopyField label="服务器地址" :value="entry.host" />
        <div v-if="entry.httpAddress || entry.socksAddress" class="credential-grid">
          <CopyField v-if="entry.httpAddress" label="HTTP 地址:端口" :value="entry.httpAddress" />
          <CopyField v-if="entry.socksAddress" label="SOCKS5 地址:端口" :value="entry.socksAddress" />
        </div>
        <CopyField v-if="entry.httpUri" label="HTTP 代理链接" :value="entry.httpUri" :display-value="entry.httpUriMasked" />
        <CopyField v-if="entry.socksUri" label="SOCKS5 代理链接" :value="entry.socksUri" :display-value="entry.socksUriMasked" />
      </section>

      <p v-if="!connections.length" class="credential-empty">该用户尚未绑定服务器，编辑用户并选择目标服务器后即可获得连接地址。</p>
    </div>

    <template #footer>
      <button class="button secondary" type="button" :disabled="!ready" @click="copyAll">
        <Check v-if="allCopied" :size="16" />
        <ClipboardCopy v-else :size="16" />
        复制全部
      </button>
      <button class="button primary" type="button" @click="$emit('close')">{{ mode === 'view' ? '关闭' : '我已保存' }}</button>
    </template>
  </ModalDialog>
</template>
