<script setup lang="ts">
import { Inbox } from 'lucide-vue-next'

withDefaults(defineProps<{
  title?: string
  description?: string
}>(), {
  title: '暂无数据',
  description: '当前筛选条件下没有可显示的记录。',
})
</script>

<template>
  <div class="empty-state">
    <span class="empty-icon"><Inbox :size="21" /></span>
    <strong>{{ title }}</strong>
    <p>{{ description }}</p>
    <slot />
  </div>
</template>
