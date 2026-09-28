<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { Check, Copy, Eye, EyeOff } from 'lucide-vue-next'

import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils'

const props = withDefaults(defineProps<{
  label: string
  /** Exact text written to the clipboard. */
  value: string
  /** Text shown instead of `value`, e.g. a proxy URL with the password masked. */
  displayValue?: string
  /** Renders the value as a password input with a show/hide toggle. */
  secret?: boolean
}>(), {
  displayValue: undefined,
  secret: false,
})

const toast = useToastStore()
const visible = ref(false)
const copied = ref(false)
let resetTimer: number | undefined

const shown = computed(() => props.displayValue ?? props.value)

async function copy(): Promise<void> {
  if (!props.value) return
  if (await copyText(props.value)) {
    copied.value = true
    window.clearTimeout(resetTimer)
    resetTimer = window.setTimeout(() => { copied.value = false }, 2_000)
  } else {
    toast.notify('error', '复制失败', '浏览器拒绝访问剪贴板，请手动选择文本复制')
  }
}

onBeforeUnmount(() => window.clearTimeout(resetTimer))
</script>

<template>
  <div class="field copy-field">
    <span>{{ label }}</span>
    <div class="secret-field">
      <input
        :type="secret && !visible ? 'password' : 'text'"
        :value="shown"
        :aria-label="label"
        readonly
        spellcheck="false"
        @focus="($event.target as HTMLInputElement).select()"
      />
      <button
        v-if="secret"
        class="icon-button"
        type="button"
        :aria-label="visible ? `隐藏${label}` : `显示${label}`"
        :title="visible ? '隐藏' : '显示'"
        @click="visible = !visible"
      >
        <EyeOff v-if="visible" :size="17" />
        <Eye v-else :size="17" />
      </button>
      <button
        class="icon-button"
        :class="{ copied }"
        type="button"
        :aria-label="`复制${label}`"
        :title="copied ? '已复制' : `复制${label}`"
        :disabled="!value"
        @click="copy"
      >
        <Check v-if="copied" :size="17" />
        <Copy v-else :size="17" />
      </button>
    </div>
  </div>
</template>
