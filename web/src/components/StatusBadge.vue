<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status: string
  compact?: boolean
}>()

const labels: Record<string, string> = {
  online: '在线',
  offline: '离线',
  unknown: '未知',
  running: '运行中',
  stopped: '已停止',
  installed: '已部署',
  deploying: '部署中',
  not_installed: '未部署',
  failed: '失败',
  synced: '已同步',
  pending: '等待同步',
  partial: '部分同步',
  queued: '排队中',
  succeeded: '已完成',
  partially_failed: '部分失败',
  cancelled: '已取消',
  active: '正常',
  disabled: '已停用',
  expired: '已到期',
  exhausted: '流量用尽',
}

const tones: Record<string, string> = {
  online: 'success',
  running: 'success',
  installed: 'success',
  synced: 'success',
  succeeded: 'success',
  active: 'success',
  deploying: 'info',
  pending: 'info',
  queued: 'neutral',
  unknown: 'neutral',
  stopped: 'neutral',
  not_installed: 'neutral',
  cancelled: 'neutral',
  disabled: 'neutral',
  partial: 'warning',
  partially_failed: 'warning',
  expired: 'warning',
  offline: 'danger',
  failed: 'danger',
  exhausted: 'danger',
}

const label = computed(() => labels[props.status] || props.status)
const tone = computed(() => tones[props.status] || 'neutral')
</script>

<template>
  <span class="status-badge" :class="[`status-${tone}`, { 'is-compact': compact }]">
    <span class="status-dot" aria-hidden="true" />
    {{ label }}
  </span>
</template>
