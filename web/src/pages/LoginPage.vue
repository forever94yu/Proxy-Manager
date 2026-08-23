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
    const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/')
      ? route.query.redirect
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
    <section class="login-surface">
      <div class="login-form-panel">
        <div class="login-brand mobile-brand">
          <span class="brand-mark"><Network :size="22" /></span>
          <div><strong>Proxy Manager</strong><span>3proxy 控制台</span></div>
        </div>

        <div class="login-form-wrap">
          <div class="login-heading">
            <span class="login-eyebrow">安全登录</span>
            <h1>进入运维控制台</h1>
          </div>

          <form class="login-form" @submit.prevent="submit">
            <label class="field">
              <span>账号</span>
              <div class="input-with-icon">
                <UserRound :size="17" />
                <input v-model="username" autocomplete="username" autofocus placeholder="输入账号" />
              </div>
            </label>

            <label class="field">
              <span>密码</span>
              <div class="input-with-icon password-input">
                <LockKeyhole :size="17" />
                <input v-model="password" :type="passwordVisible ? 'text' : 'password'" autocomplete="current-password" placeholder="输入密码" />
                <button type="button" :aria-label="passwordVisible ? '隐藏密码' : '显示密码'" :title="passwordVisible ? '隐藏密码' : '显示密码'" @click="passwordVisible = !passwordVisible">
                  <EyeOff v-if="passwordVisible" :size="17" />
                  <Eye v-else :size="17" />
                </button>
              </div>
            </label>

            <p v-if="error" class="form-alert" role="alert">{{ error }}</p>

            <button class="button primary login-submit" type="submit" :disabled="!canSubmit">
              <span v-if="loading" class="button-spinner" />
              {{ loading ? '正在登录' : '登录' }}
            </button>
          </form>
        </div>
      </div>

      <div class="login-context-panel" aria-hidden="true">
        <div class="login-brand">
          <span class="brand-mark light"><Network :size="22" /></span>
          <div><strong>Proxy Manager</strong><span>3proxy 控制台</span></div>
        </div>
        <div class="topology-scene">
          <div class="topology-ring ring-one" />
          <div class="topology-ring ring-two" />
          <span class="topology-node central"><Network :size="24" /></span>
          <span class="topology-node node-one" />
          <span class="topology-node node-two" />
          <span class="topology-node node-three" />
          <span class="topology-node node-four" />
          <span class="topology-line line-one" />
          <span class="topology-line line-two" />
          <span class="topology-line line-three" />
          <span class="topology-line line-four" />
        </div>
      </div>
    </section>
  </main>
</template>
