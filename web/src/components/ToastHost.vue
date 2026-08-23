<script setup lang="ts">
import { CheckCircle2, CircleAlert, Info, X } from 'lucide-vue-next'

import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <Teleport to="body">
    <div class="toast-host" aria-live="polite" aria-atomic="false">
      <TransitionGroup name="toast">
        <div v-for="message in toast.messages" :key="message.id" class="toast-message" :class="`toast-${message.tone}`">
          <CheckCircle2 v-if="message.tone === 'success'" :size="19" />
          <CircleAlert v-else-if="message.tone === 'error'" :size="19" />
          <Info v-else :size="19" />
          <div class="toast-copy">
            <strong>{{ message.title }}</strong>
            <p v-if="message.message">{{ message.message }}</p>
          </div>
          <button class="toast-close" type="button" aria-label="关闭通知" @click="toast.remove(message.id)"><X :size="16" /></button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
