import { createRouter, createWebHistory } from 'vue-router'

import AppShell from '@/layouts/AppShell.vue'
import DashboardPage from '@/pages/DashboardPage.vue'
import JobsPage from '@/pages/JobsPage.vue'
import LoginPage from '@/pages/LoginPage.vue'
import ServersPage from '@/pages/ServersPage.vue'
import UsersPage from '@/pages/UsersPage.vue'
import { useAuthStore } from '@/stores/auth'
import { pinia } from '@/stores'

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: LoginPage,
      meta: { public: true, title: '登录' },
    },
    {
      path: '/',
      component: AppShell,
      children: [
        { path: '', redirect: '/dashboard' },
        { path: 'dashboard', name: 'dashboard', component: DashboardPage, meta: { title: '运行概览' } },
        { path: 'servers', name: 'servers', component: ServersPage, meta: { title: '服务器' } },
        { path: 'users', name: 'users', component: UsersPage, meta: { title: '代理用户' } },
        { path: 'jobs', name: 'jobs', component: JobsPage, meta: { title: '任务记录' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore(pinia)
  await auth.bootstrap()

  if (to.meta.public) {
    if (to.name === 'login' && auth.isAuthenticated) return { name: 'dashboard' }
    return true
  }

  if (!auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }

  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : '控制台'
  document.title = `${title} · Proxy Manager`
})
