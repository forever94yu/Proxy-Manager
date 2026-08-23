<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  description?: string
  size?: 'small' | 'medium' | 'large'
  busy?: boolean
}>(), {
  description: '',
  size: 'medium',
  busy: false,
})

const emit = defineEmits<{
  close: []
}>()

const panel = ref<HTMLElement | null>(null)

function close(): void {
  if (!props.busy) emit('close')
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && props.open) close()
}

watch(() => props.open, async (open) => {
  document.body.classList.toggle('modal-open', open)
  if (open) {
    await nextTick()
    panel.value?.focus()
  }
})

onMounted(() => window.addEventListener('keydown', handleKeydown))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeydown)
  document.body.classList.remove('modal-open')
})
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="open" class="modal-layer" @mousedown.self="close">
        <section
          ref="panel"
          class="modal-panel"
          :class="`modal-${size}`"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          tabindex="-1"
        >
          <header class="modal-header">
            <div>
              <h2>{{ title }}</h2>
              <p v-if="description">{{ description }}</p>
            </div>
            <button class="icon-button" type="button" aria-label="关闭" title="关闭" :disabled="busy" @click="close">
              <X :size="18" />
            </button>
          </header>
          <div class="modal-body">
            <slot />
          </div>
          <footer v-if="$slots.footer" class="modal-footer">
            <slot name="footer" />
          </footer>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>
