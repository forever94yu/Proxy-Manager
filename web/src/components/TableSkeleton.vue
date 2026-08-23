<script setup lang="ts">
withDefaults(defineProps<{ rows?: number }>(), { rows: 5 })
</script>

<template>
  <div class="skeleton-list" aria-label="正在加载">
    <div v-for="row in rows" :key="row" class="skeleton-row">
      <span class="skeleton-block wide" />
      <span class="skeleton-block" />
      <span class="skeleton-block short" />
      <span class="skeleton-block" />
    </div>
  </div>
</template>
