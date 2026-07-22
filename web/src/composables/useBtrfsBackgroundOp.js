import { ref, onBeforeUnmount } from 'vue'

// useBtrfsBackgroundOp: 封装 btrfs 后台操作（scrub/balance）的轮询。
// fetchStatus(): Promise<{data}>  —— 查询当前状态（由调用方提供）
// isRunning(status): boolean       —— 判断是否运行中
// pollIntervalMs: 轮询间隔，默认 5000
export function useBtrfsBackgroundOp({ fetchStatus, isRunning, pollIntervalMs = 5000 }) {
  const status = ref({})
  const loading = ref(false)
  let timer = null

  function stopPoll() {
    if (timer) { clearInterval(timer); timer = null }
  }

  function schedulePoll() {
    stopPoll()
    if (isRunning(status.value)) {
      timer = setInterval(async () => {
        try {
          const res = await fetchStatus()
          status.value = res.data || {}
          if (!isRunning(status.value)) stopPoll()
        } catch { /* 静默，下轮重试 */ }
      }, pollIntervalMs)
    }
  }

  async function refresh() {
    loading.value = true
    try {
      const res = await fetchStatus()
      status.value = res.data || {}
      schedulePoll()
    } finally {
      loading.value = false
    }
  }

  onBeforeUnmount(stopPoll)
  return { status, loading, refresh, stopPoll, schedulePoll }
}
