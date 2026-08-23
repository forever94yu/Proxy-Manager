<script setup lang="ts">
import { ref, watch } from 'vue'
import { Check, Copy, Eye, EyeOff, ShieldCheck } from 'lucide-vue-next'

import ModalDialog from '@/components/ModalDialog.vue'

const props = defineProps<{
  open: boolean
  username: string
  password: string
}>()

defineEmits<{
  close: []
}>()

const visible = ref(false)
const copied = ref(false)

watch(() => props.open, (open) => {
  if (open) {
    visible.value = false
    copied.value = false
  }
})

async function copyPassword(): Promise<void> {
  await navigator.clipboard.writeText(props.password)
  copied.value = true
  window.setTimeout(() => { copied.value = false }, 2_000)
}
</script>

<template>
  <ModalDialog :open="open" title="保存代理密码" description="密码只会显示这一次，关闭后无法再次查看。" size="small" @close="$emit('close')">
    <div class="secret-notice">
      <ShieldCheck :size="20" />
      <p>账号 <strong>{{ username }}</strong> 已创建，同步任务正在后台执行。</p>
    </div>
    <label class="field">
      <span>生成的密码</span>
      <div class="secret-field">
        <input :type="visible ? 'text' : 'password'" :value="password" readonly spellcheck="false" />
        <button class="icon-button" type="button" :aria-label="visible ? '隐藏密码' : '显示密码'" :title="visible ? '隐藏密码' : '显示密码'" @click="visible = !visible">
          <EyeOff v-if="visible" :size="17" />
          <Eye v-else :size="17" />
        </button>
        <button class="icon-button" type="button" aria-label="复制密码" title="复制密码" @click="copyPassword">
          <Check v-if="copied" :size="17" />
          <Copy v-else :size="17" />
        </button>
      </div>
    </label>
    <template #footer>
      <button class="button primary" type="button" @click="$emit('close')">我已保存</button>
    </template>
  </ModalDialog>
</template>
