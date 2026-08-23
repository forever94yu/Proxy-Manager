<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  Activity,
  LayoutDashboard,
  ListChecks,
  LogOut,
  Menu,
  Network,
  Server,
  UsersRound,
  X,
} from 'lucide-vue-next'

import { useAuthStore } from '@/stores/auth'
import { useToastStore } from '@/stores/toast'
import { operatorInitials } from '@/utils'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const toast = useToastStore()
const mobileOpen = ref(false)
const loggingOut = ref(false)

const navItems = [
  { to: '/dashboard', label: '运行概览', icon: LayoutDashboard },
  { to: '/servers', label: '服务器', icon: Server },
  { to: '/users', label: '代理用户', icon: UsersRound },
  { to: '/jobs', label: '任务记录', icon: ListChecks },
]

const pageTitle = computed(() => typeof route.meta.title === 'string' ? route.meta.title : '控制台')
const roleLabel = computed(() => ({ admin: '管理员', operator: '运维人员', viewer: '只读成员' })[auth.operator?.role || 'viewer'])

watch(() => route.fullPath, () => { mobileOpen.value = false })

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') mobileOpen.value = false
}

async function logout(): Promise<void> {
  loggingOut.value = true
  try {
    await auth.logout()
    await router.replace('/login')
  } catch (error) {
    toast.notify('error', '退出失败', error instanceof Error ? error.message : '请稍后重试')
  } finally {
    loggingOut.value = false
  }
}

onMounted(() => window.addEventListener('keydown', handleKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', handleKeydown))
</script>

<template>
  <div class="app-shell">
    <Transition name="fade">
      <button v-if="mobileOpen" class="sidebar-backdrop" type="button" aria-label="关闭导航" @click="mobileOpen = false" />
    </Transition>

    <aside class="sidebar" :class="{ open: mobileOpen }">
      <div class="brand-row">
        <span class="brand-mark"><Network :size="21" /></span>
        <div class="brand-copy">
          <strong>Proxy Manager</strong>
          <span>3proxy 控制台</span>
        </div>
        <button class="sidebar-close" type="button" aria-label="关闭导航" @click="mobileOpen = false"><X :size="19" /></button>
      </div>

      <nav class="main-nav" aria-label="主导航">
        <span class="nav-label">工作台</span>
        <RouterLink v-for="item in navItems" :key="item.to" :to="item.to" class="nav-item">
          <component :is="item.icon" :size="18" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="sidebar-footer">
        <div class="control-health">
          <span class="health-icon"><Activity :size="16" /></span>
          <div>
            <strong>管理服务正常</strong>
            <span>API 已连接</span>
          </div>
          <span class="health-dot" aria-label="在线" />
        </div>
        <div class="operator-block">
          <span class="operator-avatar">{{ operatorInitials(auth.operator?.name || '') }}</span>
          <div class="operator-copy">
            <strong>{{ auth.operator?.name || auth.operator?.username }}</strong>
            <span>{{ roleLabel }}</span>
          </div>
          <button class="icon-button" type="button" aria-label="退出登录" title="退出登录" :disabled="loggingOut" @click="logout">
            <LogOut :size="17" />
          </button>
        </div>
      </div>
    </aside>

    <div class="workspace">
      <header class="topbar">
        <div class="topbar-leading">
          <button class="mobile-menu-button" type="button" aria-label="打开导航" @click="mobileOpen = true"><Menu :size="20" /></button>
          <div>
            <span class="topbar-context">Proxy Manager</span>
            <h1>{{ pageTitle }}</h1>
          </div>
        </div>
        <div class="topbar-status">
          <span class="health-dot" />
          控制面在线
        </div>
      </header>

      <main class="page-content">
        <RouterView />
      </main>
    </div>
  </div>
</template>
