<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertCircle, RefreshCw, Rss, Server as ServerIcon } from 'lucide-vue-next'

import { usersApi } from '@/api'
import CopyField from '@/components/CopyField.vue'
import QrCode from '@/components/QrCode.vue'
import { useToastStore } from '@/stores/toast'
import type { Server } from '@/types'
import {
  buildNodeShareLink,
  buildShadowrocketNodeLink,
  buildShadowrocketSubscriptionLink,
  formatHostPort,
  resolveSubscriptionUrl,
} from '@/utils'

/**
 * QR codes that mobile proxy clients scan to import a proxy user.
 *
 * - Node: one server's SOCKS5 endpoint as a share link. Works without access
 *   to the console, but the client keeps a copy that goes stale when the
 *   password or server changes.
 * - Subscription: a URL listing every bound server. Clients refresh it, so
 *   changes reach them; the phone must be able to reach the console.
 *
 * One server defaults to a node and several to a subscription; the operator
 * can switch either way.
 */

type ImportMode = 'node' | 'subscription'
type ClientKind = 'generic' | 'shadowrocket' | 'clash'

const props = defineProps<{
  /** Absent right after creation if the server did not echo the user. */
  userId?: string
  username: string
  password: string
  servers: Server[]
}>()

const CLIENTS: { value: ClientKind; label: string }[] = [
  { value: 'generic', label: 'v2rayNG / NekoBox' },
  { value: 'shadowrocket', label: 'Shadowrocket' },
  { value: 'clash', label: 'Clash' },
]

const toast = useToastStore()
const mode = ref<ImportMode>('node')
const client = ref<ClientKind>('generic')
const selectedServerId = ref('')
const subscriptionUrl = ref('')
const subscriptionLoading = ref(false)
const subscriptionError = ref('')
const confirmingReset = ref(false)
const resetting = ref(false)
let subscriptionRequest = 0

/** Node QR codes carry SOCKS5, the protocol every supported client imports. */
const nodeServers = computed(() => props.servers.filter((server) => server.socksPort))
const selectedServer = computed(() => nodeServers.value.find((server) => server.id === selectedServerId.value) || nodeServers.value[0])

watch(() => props.servers.map((server) => server.id).join(','), () => {
  mode.value = props.servers.length > 1 || !nodeServers.value.length ? 'subscription' : 'node'
  if (!nodeServers.value.some((server) => server.id === selectedServerId.value)) selectedServerId.value = nodeServers.value[0]?.id || ''
}, { immediate: true })

/** Clash scanners create a profile from a URL, so Clash only imports subscriptions. */
const nodeUnsupported = computed(() => mode.value === 'node' && client.value === 'clash')

const nodeLink = computed(() => {
  const server = selectedServer.value
  if (!server?.socksPort || !props.password) return ''
  const build = client.value === 'shadowrocket' ? buildShadowrocketNodeLink : buildNodeShareLink
  return build(server.name, props.username, props.password, server.host, server.socksPort)
})

const qrValue = computed(() => {
  if (mode.value === 'node') return nodeUnsupported.value ? '' : nodeLink.value
  if (!subscriptionUrl.value) return ''
  return client.value === 'shadowrocket'
    ? buildShadowrocketSubscriptionLink(subscriptionUrl.value, props.username)
    : subscriptionUrl.value
})

const qrLabel = computed(() => mode.value === 'node'
  ? `${selectedServer.value?.name || ''} 节点二维码`
  : `${props.username} 订阅二维码`)

/** Why the subscription URL may not work on the phone, if anything. */
const subscriptionWarning = computed(() => {
  if (!subscriptionUrl.value) return ''
  const { protocol, hostname } = new URL(subscriptionUrl.value)
  // e.g. the console is reached through an SSH tunnel
  if (hostname === 'localhost' || hostname.endsWith('.localhost') || hostname === '[::1]' || hostname.startsWith('127.')) {
    return '当前通过本机地址访问控制台，手机无法访问此订阅地址。请在服务端设置 PUBLIC_URL 为手机可以访问的控制台地址。'
  }
  // v2rayNG refuses plain HTTP subscriptions except on private networks.
  const privateIPv4 = /^(10|127)\.|^192\.168\.|^172\.(1[6-9]|2\d|3[01])\./.test(hostname)
  if (protocol === 'http:' && client.value === 'generic' && !privateIPv4) {
    return 'v2rayNG 只接受 HTTPS 订阅地址（局域网 IP 除外）。请通过 HTTPS 访问控制台，或把 PUBLIC_URL 设置为 HTTPS 地址。'
  }
  return ''
})

const scanHint = computed(() => {
  if (client.value === 'shadowrocket') {
    return mode.value === 'node'
      ? '打开 Shadowrocket，点首页左上角的扫码图标。'
      : '用 Shadowrocket 首页左上角扫码，或用 iPhone 相机扫码后在 Shadowrocket 中打开。'
  }
  if (client.value === 'clash') return 'Clash Meta for Android：新建配置时选择“从二维码导入”；FlClash：添加配置时选择“二维码”。'
  return mode.value === 'node'
    ? 'v2rayNG：点右上角 + → 扫描二维码；NekoBox：在添加菜单中选择扫描二维码。'
    : 'v2rayNG：点右上角 + → 扫描二维码，然后在订阅分组中更新订阅；NekoBox：在添加菜单中选择扫描二维码。'
})

async function loadSubscription(): Promise<void> {
  if (!props.userId) {
    subscriptionError.value = '无法获取订阅地址，请关闭后在“连接信息”中重新打开'
    return
  }
  const request = ++subscriptionRequest
  subscriptionLoading.value = true
  subscriptionError.value = ''
  try {
    const subscription = await usersApi.subscription(props.userId)
    if (request === subscriptionRequest) subscriptionUrl.value = resolveSubscriptionUrl(subscription)
  } catch (caught) {
    if (request === subscriptionRequest) subscriptionError.value = caught instanceof Error ? caught.message : '订阅地址加载失败'
  } finally {
    if (request === subscriptionRequest) subscriptionLoading.value = false
  }
}

async function resetSubscription(): Promise<void> {
  if (!props.userId) return
  const request = ++subscriptionRequest
  resetting.value = true
  try {
    const subscription = await usersApi.resetSubscription(props.userId)
    if (request !== subscriptionRequest) return
    subscriptionUrl.value = resolveSubscriptionUrl(subscription)
    subscriptionError.value = ''
    confirmingReset.value = false
    toast.notify('success', '订阅地址已重置', '旧地址已失效，请让使用者重新扫码导入')
  } catch (caught) {
    toast.notify('error', '重置失败', caught instanceof Error ? caught.message : '请稍后重试')
  } finally {
    if (request === subscriptionRequest) resetting.value = false
  }
}

// The subscription URL is fetched only when needed: every fetch is audited.
watch([mode, () => props.userId], () => {
  if (mode.value === 'subscription' && !subscriptionUrl.value && !subscriptionLoading.value) void loadSubscription()
}, { immediate: true })
</script>

<template>
  <section class="credential-section qr-import">
    <div class="qr-import-heading">
      <h3>扫码导入</h3>
      <div class="segmented-control" role="radiogroup" aria-label="导入方式">
        <button type="button" role="radio" :aria-checked="mode === 'node'" :class="{ active: mode === 'node' }" :disabled="!nodeServers.length" @click="mode = 'node'">
          <ServerIcon :size="15" /> 单个节点
        </button>
        <button type="button" role="radio" :aria-checked="mode === 'subscription'" :class="{ active: mode === 'subscription' }" @click="mode = 'subscription'">
          <Rss :size="15" /> 订阅
        </button>
      </div>
    </div>

    <p class="field-hint">
      <template v-if="mode === 'node'">二维码包含一台服务器的 SOCKS5 连接信息，导入后不需要访问控制台；修改密码或服务器后需要重新扫码。</template>
      <template v-else>订阅包含全部 {{ servers.length }} 台服务器的 SOCKS5 和 HTTP 节点。客户端更新订阅即可同步密码和服务器变更，但手机必须能访问控制台地址。</template>
    </p>

    <div class="qr-import-controls">
      <label class="field">
        <span>客户端</span>
        <select v-model="client" class="filter-select">
          <option v-for="option in CLIENTS" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
      </label>
      <label v-if="mode === 'node' && nodeServers.length > 1" class="field">
        <span>服务器</span>
        <select v-model="selectedServerId" class="filter-select">
          <option v-for="server in nodeServers" :key="server.id" :value="server.id">{{ server.name }}</option>
        </select>
      </label>
    </div>

    <div class="qr-import-body">
      <div class="qr-frame">
        <QrCode v-if="qrValue" :value="qrValue" :label="qrLabel" />
        <span v-else-if="mode === 'subscription' && subscriptionLoading" class="skeleton-block qr-skeleton" aria-busy="true" aria-label="正在加载订阅地址" />
        <p v-else-if="nodeUnsupported" class="qr-placeholder">Clash 只能通过订阅导入，请切换到“订阅”。</p>
        <p v-else-if="mode === 'subscription' && subscriptionError" class="qr-placeholder qr-error">
          <AlertCircle :size="16" aria-hidden="true" /><span>{{ subscriptionError }}</span>
          <button v-if="userId" class="text-button" type="button" @click="loadSubscription">重试</button>
        </p>
        <p v-else class="qr-placeholder">暂无可用的连接信息</p>
      </div>

      <div class="qr-import-details">
        <p class="qr-scan-hint">{{ scanHint }}</p>
        <template v-if="mode === 'node'">
          <p v-if="selectedServer" class="qr-node-summary">
            {{ selectedServer.name }} · SOCKS5 {{ formatHostPort(selectedServer.host, selectedServer.socksPort || 0) }}
          </p>
          <CopyField v-if="nodeLink && !nodeUnsupported" label="节点链接" :value="nodeLink" display-value="socks://••••••" />
        </template>
        <template v-else-if="subscriptionUrl">
          <CopyField label="订阅地址" :value="subscriptionUrl" />
          <p v-if="subscriptionWarning" class="field-warning">{{ subscriptionWarning }}</p>
          <p v-else class="field-hint">订阅地址相当于账号密码，请勿公开分享。</p>
          <div v-if="confirmingReset" class="qr-reset-confirm">
            <span>重置后旧地址立即失效，已导入的客户端需要重新扫码。</span>
            <button class="text-button" type="button" :disabled="resetting" @click="resetSubscription">
              <RefreshCw v-if="resetting" :size="13" class="spinning" />确认重置
            </button>
            <button class="text-button" type="button" :disabled="resetting" @click="confirmingReset = false">取消</button>
          </div>
          <button v-else class="text-button qr-reset" type="button" @click="confirmingReset = true">
            <RefreshCw :size="13" />重置订阅地址
          </button>
        </template>
      </div>
    </div>
  </section>
</template>
