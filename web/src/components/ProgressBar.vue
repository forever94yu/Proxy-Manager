<script setup lang="ts">
import { computed } from 'vue'

import { clampProgress } from '@/utils'

const props = withDefaults(defineProps<{
  value?: number
  tone?: 'default' | 'danger' | 'warning'
}>(), {
  value: 0,
  tone: 'default',
})

const progress = computed(() => clampProgress(props.value))
</script>

<template>
  <div class="progress-track" :class="`progress-${tone}`" role="progressbar" :aria-valuenow="progress" aria-valuemin="0" aria-valuemax="100">
    <span class="progress-fill" :style="{ width: `${progress}%` }" />
  </div>
</template>
