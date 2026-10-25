import { ref, onMounted, onUnmounted } from 'vue'

// useLive subscribes to the server event stream (SSE) and invokes onEvent for
// every message. It reconnects automatically with a small backoff.
export function useLive(onEvent, options = {}) {
  const connected = ref(false)
  const lastError = ref('')
  let source = null
  let closed = false
  let retry = 0
  let timer = null

  const handlers = ['log', 'changed', 'scan-progress', 'hosts', 'hello']

  function connect() {
    if (closed) return
    try {
      source = new EventSource('/api/logs/stream', { withCredentials: true })
    } catch (err) {
      lastError.value = String(err)
      schedule()
      return
    }
    source.onopen = () => {
      connected.value = true
      retry = 0
      lastError.value = ''
    }
    source.onerror = () => {
      connected.value = false
      lastError.value = '连接已断开，正在重连…'
      if (source) {
        source.close()
        source = null
      }
      schedule()
    }
    for (const type of handlers) {
      source.addEventListener(type, (ev) => {
        let data = {}
        try {
          data = ev.data ? JSON.parse(ev.data) : {}
        } catch {
          data = { raw: ev.data }
        }
        if (onEvent) onEvent({ type, data })
      })
    }
  }

  function schedule() {
    if (closed) return
    retry += 1
    const delay = Math.min(15000, 1000 * retry)
    clearTimeout(timer)
    timer = setTimeout(connect, delay)
  }

  onMounted(() => {
    if (options.immediate !== false) connect()
  })
  onUnmounted(() => {
    closed = true
    clearTimeout(timer)
    if (source) source.close()
  })

  return { connected, lastError }
}
