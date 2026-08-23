import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import { authApi } from '@/api'
import type { Operator } from '@/types'

export const useAuthStore = defineStore('auth', () => {
  const operator = ref<Operator | null>(null)
  const initialized = ref(false)
  const isAuthenticated = computed(() => operator.value !== null)

  async function bootstrap(): Promise<void> {
    if (initialized.value) return
    try {
      operator.value = await authApi.me()
    } catch {
      operator.value = null
    } finally {
      initialized.value = true
    }
  }

  async function login(username: string, password: string): Promise<void> {
    operator.value = await authApi.login(username, password)
    initialized.value = true
  }

  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } finally {
      operator.value = null
      initialized.value = true
    }
  }

  return {
    operator,
    initialized,
    isAuthenticated,
    bootstrap,
    login,
    logout,
  }
})
