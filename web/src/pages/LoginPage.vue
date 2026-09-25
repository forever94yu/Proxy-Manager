<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Eye, EyeOff, LockKeyhole, Network, UserRound } from 'lucide-vue-next'

import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const username = ref('')
const password = ref('')
const passwordVisible = ref(false)
const loading = ref(false)
const error = ref('')
const canSubmit = computed(() => username.value.trim().length > 0 && password.value.length > 0 && !loading.value)

async function submit(): Promise<void> {
  if (!canSubmit.value) return
  loading.value = true
  error.value = ''
  try {
    await auth.login(username.value.trim(), password.value)
    const target = route.query.redirect
    const redirect = typeof target === 'string'
      && target.startsWith('/')
      && !target.startsWith('//')
      && !target.startsWith('/\\')
      && !target.startsWith('/login')
      ? target
      : '/dashboard'
    await router.replace(redirect)
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '登录失败，请稍后重试'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-card">
      <span class="app-icon large" aria-hidden="true"><Network :size="34" /></span>
      <h1>登录 Proxy Manager</h1>
      <p class="login-lede">管理你的 3proxy 服务器与代理账号</p>

      <form class="login-form" @submit.prevent="submit">
        <div class="input-group" :class="{ invalid: error }">
          <label class="input-row">
            <UserRound :size="18" aria-hidden="true" />
            <span class="visually-hidden">账号</span>
            <input v-model="username" autocomplete="username" autofocus placeholder="账号" />
          </label>
          <label class="input-row">
            <LockKeyhole :size="18" aria-hidden="true" />
            <span class="visually-hidden">密码</span>
            <input v-model="password" :type="passwordVisible ? 'text' : 'password'" autocomplete="current-password" placeholder="密码" />
            <button class="reveal-button" type="button" :aria-label="passwordVisible ? '隐藏密码' : '显示密码'" :title="passwordVisible ? '隐藏密码' : '显示密码'" @click="passwordVisible = !passwordVisible">
              <EyeOff v-if="passwordVisible" :size="17" />
              <Eye v-else :size="17" />
            </button>
          </label>
        </div>

        <p v-if="error" class="form-alert" role="alert">{{ error }}</p>

        <button class="button primary login-submit" type="submit" :disabled="!canSubmit">
          <span v-if="loading" class="button-spinner" />
          {{ loading ? '正在登录' : '登录' }}
        </button>
      </form>
    </section>
  </main>
</template>
