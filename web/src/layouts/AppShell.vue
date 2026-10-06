<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  CircleArrowUp,
  LayoutDashboard,
  ListChecks,
  LogOut,
  Menu,
  Network,
  Server,
  UsersRound,
  X,
} from 'lucide-vue-next'

import { systemApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useToastStore } from '@/stores/toast'
import { operatorInitials } from '@/utils'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const toast = useToastStore()
const mobileOpen = ref(false)
const loggingOut = ref(false)
const scrolled = ref(false)
const currentVersion = ref('')
const updateAvailable = ref(false)

const navItems = [
  { to: '/dashboard', label: '运行概览', icon: LayoutDashboard },
  { to: '/servers', label: '服务器', icon: Server },
  { to: '/users', label: '代理用户', icon: UsersRound },
  { to: '/jobs', label: '任务记录', icon: ListChecks },
  { to: '/update', label: '系统更新', icon: CircleArrowUp },
]

const pageTitle = computed(() => typeof route.meta.title === 'string' ? route.meta.title : '控制台')
const roleLabel = computed(() => ({ admin: '管理员', operator: '运维人员', viewer: '只读成员' })[auth.operator?.role || 'viewer'])

watch(() => route.fullPath, () => { mobileOpen.value = false })

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') mobileOpen.value = false
}

// The page's large title scrolls away; once it has, the compact title fades
// into the translucent top bar (the large-title pattern from Apple's apps).
function handleScroll(): void {
  scrolled.value = window.scrollY > 44
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

// The server caches the GitHub check for an hour, so this is cheap.
async function loadVersion(): Promise<void> {
  try {
    const status = await systemApi.update()
    currentVersion.value = status.currentVersion
    updateAvailable.value = status.enabled && status.updateAvailable
  } catch {
    // The version badge is informational only.
  }
}

onMounted(() => {
  loadVersion()
  window.addEventListener('keydown', handleKeydown)
  window.addEventListener('scroll', handleScroll, { passive: true })
  handleScroll()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('scroll', handleScroll)
})
</script>

<template>
  <div class="app-shell">
    <Transition name="fade">
      <button v-if="mobileOpen" class="sidebar-backdrop" type="button" aria-label="关闭导航" @click="mobileOpen = false" />
    </Transition>

    <aside class="sidebar" :class="{ open: mobileOpen }">
      <div class="brand-row">
        <span class="app-icon small"><Network :size="17" /></span>
        <strong class="brand-name">Proxy Manager</strong>
        <button class="icon-button sidebar-close" type="button" aria-label="关闭导航" @click="mobileOpen = false"><X :size="18" /></button>
      </div>

      <nav class="main-nav" aria-label="主导航">
        <RouterLink v-for="item in navItems" :key="item.to" :to="item.to" class="nav-item">
          <component :is="item.icon" :size="18" />
          <span>{{ item.label }}</span>
          <i v-if="item.to === '/update' && updateAvailable" class="nav-dot" title="有新版本可用" />
        </RouterLink>
      </nav>

      <div class="sidebar-footer">
        <RouterLink v-if="currentVersion" to="/update" class="version-tag" :title="updateAvailable ? '有新版本可用' : '当前版本'">
          v{{ currentVersion }}<span v-if="updateAvailable">· 可升级</span>
        </RouterLink>
        <div class="operator-block">
          <span class="operator-avatar" aria-hidden="true">{{ operatorInitials(auth.operator?.name || '') }}</span>
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
      <header class="topbar" :class="{ scrolled }">
        <button class="icon-button mobile-menu-button" type="button" aria-label="打开导航" @click="mobileOpen = true"><Menu :size="20" /></button>
        <h1 class="topbar-title">{{ pageTitle }}</h1>
      </header>

      <main class="page-content">
        <RouterView />
      </main>
    </div>
  </div>
</template>
