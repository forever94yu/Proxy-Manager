import { createApp } from 'vue'

import App from '@/App.vue'
import { UNAUTHORIZED_EVENT } from '@/api/client'
import { router } from '@/router'
import { pinia } from '@/stores'
import { useAuthStore } from '@/stores/auth'
import '@/styles.css'

// A protected API call returned 401 (session expired / secret rotated):
// drop the cached operator and send the user back to the login page.
window.addEventListener(UNAUTHORIZED_EVENT, () => {
  useAuthStore(pinia).clearSession()
  const current = router.currentRoute.value
  if (current.meta.public) return
  void router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

createApp(App).use(pinia).use(router).mount('#app')
