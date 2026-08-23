import { ref } from 'vue'
import { defineStore } from 'pinia'

export type ToastTone = 'success' | 'error' | 'info'

export interface ToastMessage {
  id: number
  tone: ToastTone
  title: string
  message?: string
}

let nextId = 0

export const useToastStore = defineStore('toast', () => {
  const messages = ref<ToastMessage[]>([])

  function notify(tone: ToastTone, title: string, message?: string): void {
    const id = ++nextId
    messages.value.push({ id, tone, title, message })
    window.setTimeout(() => remove(id), tone === 'error' ? 6_000 : 4_000)
  }

  function remove(id: number): void {
    messages.value = messages.value.filter((message) => message.id !== id)
  }

  return { messages, notify, remove }
})
