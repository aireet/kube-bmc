import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import { Poller } from './poller'

/** Polls fn every `ms` (less often while the page is hidden). The last successful result is kept on error. */
export function usePoll<T>(fn: () => Promise<T>, ms: number) {
  const data = ref<T>() as Ref<T | undefined>
  const error = ref<string>()
  const loading = ref(true)
  const updatedAt = ref<Date>()

  const poller = new Poller<T>({
    fetch: fn,
    interval: ms,
    hiddenFactor: 4,
    isHidden: () => document.hidden,
    onResult: (r) => {
      if ('value' in r) {
        data.value = r.value
        error.value = undefined
        updatedAt.value = new Date()
      } else {
        error.value = r.error instanceof Error ? r.error.message : String(r.error)
      }
      loading.value = false
    },
  })

  onMounted(() => void poller.refresh())
  onBeforeUnmount(() => poller.stop())
  return { data, error, loading, updatedAt, refresh: () => poller.refresh() }
}
