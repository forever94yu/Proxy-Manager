<script setup lang="ts">
import { AlertTriangle } from 'lucide-vue-next'

import ModalDialog from '@/components/ModalDialog.vue'

withDefaults(defineProps<{
  open: boolean
  title: string
  description: string
  detail?: string
  confirmLabel?: string
  busy?: boolean
  danger?: boolean
}>(), {
  detail: '',
  confirmLabel: '确认',
  busy: false,
  danger: false,
})

defineEmits<{
  close: []
  confirm: []
}>()
</script>

<template>
  <ModalDialog :open="open" :title="title" size="small" :busy="busy" @close="$emit('close')">
    <div class="confirm-content">
      <span class="confirm-icon" :class="{ danger }"><AlertTriangle :size="20" /></span>
      <div>
        <p>{{ description }}</p>
        <p v-if="detail" class="confirm-detail">{{ detail }}</p>
      </div>
    </div>
    <template #footer>
      <button class="button secondary" type="button" :disabled="busy" @click="$emit('close')">取消</button>
      <button class="button" :class="danger ? 'danger' : 'primary'" type="button" :disabled="busy" @click="$emit('confirm')">
        <span v-if="busy" class="button-spinner" />
        {{ busy ? '处理中' : confirmLabel }}
      </button>
    </template>
  </ModalDialog>
</template>
