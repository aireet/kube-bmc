import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

/** Polls fn every `ms` (less often while the page is hidden). The last successful result is kept on error. */
export function usePoll<T>(fn: () => Promise<T>, ms: number) {
  const data = ref<T>() as Ref<T | undefined>
  const error = ref<string>()
  const loading = ref(true)
  const updatedAt = ref<Date>()
  let timer: number | undefined
  let stopped = false

  async function tick() {
    window.clearTimeout(timer)
    try {
      data.value = await fn()
      error.value = undefined
      updatedAt.value = new Date()
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
      if (!stopped) timer = window.setTimeout(tick, document.hidden ? ms * 4 : ms)
    }
  }

  onMounted(tick)
  onBeforeUnmount(() => {
    stopped = true
    window.clearTimeout(timer)
  })
  return { data, error, loading, updatedAt, refresh: tick }
}
