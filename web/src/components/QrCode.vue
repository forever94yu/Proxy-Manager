<script setup lang="ts">
import { computed } from 'vue'
import { encode } from 'uqr'

const props = defineProps<{
  value: string
  /** Accessible description of what the code contains. */
  label: string
}>()

/** Dark modules as one SVG path; the quiet zone comes from CSS padding. */
const code = computed(() => {
  const { size, data } = encode(props.value, { ecc: 'M', border: 0 })
  let path = ''
  data.forEach((row, y) => {
    row.forEach((dark, x) => {
      if (dark) path += `M${x} ${y}h1v1h-1z`
    })
  })
  return { size, path }
})
</script>

<template>
  <svg
    class="qr-code"
    :viewBox="`0 0 ${code.size} ${code.size}`"
    role="img"
    :aria-label="label"
    shape-rendering="crispEdges"
  >
    <path :d="code.path" />
  </svg>
</template>
