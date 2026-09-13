import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { apiClient } from '@/api/client'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import {
  publicProductGoodsKey,
  publicProductHref,
  publicProductQuoteTime,
  type PublicProductQuote,
} from '@/utils/publicProductCatalog'

const SUCCESS_TTL = 60_000
const ATTEMPT_INTERVAL = 15_000
const TIMEOUT = 120_000

export type PublicProductQuoteResult =
  | { kind: 'success'; url: string }
  | { kind: 'unavailable' | 'invalid' | 'cancelled' }
  | { kind: 'failed'; url: string; reason: string }

type Intent = 'automatic' | 'click' | 'refresh'
interface Task {
  key: string
  product: PublicAccountImportProduct
  manual: boolean
  running: boolean
  done: boolean
  requested: boolean
  controller: AbortController
  timer?: ReturnType<typeof setTimeout>
  pollTimer?: ReturnType<typeof setTimeout>
  deadline: number
  manualDeadline?: number
  promise: Promise<PublicProductQuoteResult>
  resolve: (result: PublicProductQuoteResult) => void
}

function identity(product: PublicAccountImportProduct): string {
  return JSON.stringify([product.id, product.shop_id, publicProductGoodsKey(product.url) || product.url])
}

class PressureError extends Error {
  constructor(message: string, readonly delay = SUCCESS_TTL) { super(message) }
}

/** One scheduler and memory-only quote overlay per mounted catalog page. */
export function usePublicProductQuotes(catalog: Ref<PublicAccountImportProduct[]>) {
  const overrides = ref<Record<string, PublicProductQuote>>({})
  const states = ref<Record<string, { status: 'checking' | 'failed'; at: number }>>({})
  const unavailable = ref<Record<string, number>>({})
  const clock = ref(Date.now())
  const pausedUntil = ref(0)
  const busy = ref(new Set<string>())
  const tasks = new Map<string, Task>()
  const attempts = new Map<string, { at: number; result?: PublicProductQuoteResult }>()
  const current = computed(() => new Map(catalog.value.map(product => [identity(product), product])))
  let running = 0
  let disposed = false
  const clockTimer = setInterval(() => { clock.value = Date.now() }, 1_000)

  const products = computed(() => catalog.value.flatMap(product => {
    const key = identity(product)
    if (unavailable.value[key] !== undefined) return []
    return [{ ...product, ...overrides.value[key] }]
  }))

  function status(product: PublicAccountImportProduct) {
    const state = states.value[identity(product)]
    const verifiedAt = publicProductQuoteTime(product)
    if (state && state.at >= verifiedAt) return state.status
    if (!verifiedAt) return 'idle'
    const now = Math.max(clock.value, Date.now())
    return now >= verifiedAt && now - verifiedAt < SUCCESS_TTL ? 'verified' : 'historical'
  }

  function refreshDisabled(product: PublicAccountImportProduct): boolean {
    const key = identity(product)
    const attempt = attempts.get(key)
    // Read the clock even for a pending task so cooldowns update in the UI.
    const now = clock.value
    return Boolean(busy.value.has(key) || (attempt && now - attempt.at < ATTEMPT_INTERVAL))
  }

  function refreshCooldown(product: PublicAccountImportProduct): number {
    const attempt = attempts.get(identity(product))
    if (!attempt) return 0
    return Math.max(0, Math.ceil((ATTEMPT_INTERVAL - (clock.value - attempt.at)) / 1_000))
  }

  function finish(task: Task, result: PublicProductQuoteResult, quote?: PublicProductQuote) {
    if (task.done) return
    task.done = true
    clearTimeout(task.timer)
    clearTimeout(task.pollTimer)
    task.controller.abort()
    tasks.delete(task.key)
    busy.value.delete(task.key)
    if (task.running) running--
    if (!disposed && current.value.has(task.key)) {
      clock.value = Date.now()
      delete states.value[task.key]
      if (quote) {
        const base = current.value.get(task.key)!
        if (publicProductQuoteTime(base) <= Date.parse(quote.quote_verified_at)) overrides.value[task.key] = quote
      } else if (result.kind === 'unavailable') {
        unavailable.value[task.key] = Date.now()
      } else if (result.kind === 'failed' || result.kind === 'invalid') {
        states.value[task.key] = { status: 'failed', at: Date.now() }
      }
      const attempt = attempts.get(task.key)
      if (attempt && result.kind !== 'cancelled') attempt.result = result
    }
    task.resolve(result)
    pumpTasks()
  }

  function armDeadline(task: Task, deadline: number) {
    task.deadline = Math.min(task.deadline, deadline)
    clearTimeout(task.timer)
    task.timer = setTimeout(() => finish(task, {
      kind: 'failed', url: publicProductHref(task.product.url), reason: 'timeout',
    }), Math.max(0, task.deadline - Date.now()))
  }

  async function execute(task: Task) {
    const url = publicProductHref(task.product.url)
    try {
      // The browser talks only to the same-origin task API.  The worker owns
      // all upstream requests and publishes the resulting quote to the
      // catalog consumed below.
      const accepted = await apiClient.post('/public/account-import/products/refresh-one', {
        shop_id: task.product.shop_id,
        product_id: task.product.id,
      })
      if (task.done) return
      let state = accepted.data?.state || 'queued'
      const started = Date.now()
      while (!task.done && Date.now() - started < 120_000 && (state === 'queued' || state === 'running')) {
        // Poll once per second through the first ten seconds, then back off
        // to five-second probes for the remainder of the two-minute window.
        await new Promise<void>(resolve => {
          task.pollTimer = setTimeout(() => {
            task.pollTimer = undefined
            resolve()
          }, Date.now() - started <= 10_000 ? 1_000 : 5_000)
        })
        if (task.done) return
        const statusResponse = await apiClient.get('/public/account-import/products/refresh-one/status', {
          params: { shop_id: task.product.shop_id, product_id: task.product.id },
        })
        state = statusResponse.data?.state || 'idle'
      }
      if (state === 'succeeded' || state === 'superseded') {
        // A successful worker commit and a superseded result both require a
        // fresh catalog read.  Merge only the requested product so unrelated
        // cards retain their local overlays and ordering.
        const catalogResponse = await apiClient.get('/public/account-import/products')
        const latest = (catalogResponse.data?.products || []).find((item: PublicAccountImportProduct) =>
          item.id === task.product.id && item.shop_id === task.product.shop_id
        )
        if (latest) {
          const index = catalog.value.findIndex(item => item.id === latest.id && item.shop_id === latest.shop_id)
          if (index >= 0) catalog.value[index] = latest
        }
        if (state === 'superseded') {
          finish(task, { kind: 'failed', url, reason: 'superseded' })
          return
        }
        finish(task, { kind: 'success', url }); return
      }
      if (state === 'unavailable') { finish(task, { kind: 'unavailable' }); return }
      if (state === 'queued' || state === 'running') { finish(task, { kind: 'failed', url, reason: 'timeout' }); return }
      finish(task, { kind: 'failed', url, reason: 'query-failed' })
    } catch (error) {
      if (task.done) return
      if (error instanceof PressureError) pausedUntil.value = Math.max(pausedUntil.value, Date.now() + error.delay)
      finish(task, { kind: 'failed', url, reason: error instanceof Error ? error.message : 'query-failed' })
      if (error instanceof PressureError) cancelAutomatic()
    }
  }

  function pumpTasks() {
    if (disposed) return
    while (running < 2) {
      const queue = [...tasks.values()].filter(task => !task.running && !task.done && current.value.has(task.key)
        && (task.manual || Date.now() >= pausedUntil.value))
      const task = queue.find(task => task.manual) || queue[0]
      if (!task) return
      if (task.deadline <= Date.now()) {
        finish(task, { kind: 'failed', url: publicProductHref(task.product.url), reason: 'timeout' })
        continue
      }
      task.running = true
      running++
      if (!task.requested) {
        task.requested = true
        attempts.set(task.key, { at: Date.now() })
      }
      states.value[task.key] = { status: 'checking', at: Date.now() }
      armDeadline(task, Date.now() + TIMEOUT)
      void execute(task)
    }
  }

  function request(product: PublicAccountImportProduct, intent: Intent = 'automatic'): Promise<PublicProductQuoteResult> {
    const key = identity(product)
    const url = publicProductHref(product.url)
    if (disposed || !current.value.has(key)) return Promise.resolve({ kind: 'cancelled' })
    if (!url) {
      states.value[key] = { status: 'failed', at: Date.now() }
      return Promise.resolve({ kind: 'invalid' })
    }
    if (unavailable.value[key] !== undefined) return Promise.resolve({ kind: 'unavailable' })
    const existing = tasks.get(key)
    if (existing) {
      if (intent !== 'automatic' && !existing.manual) {
        existing.manual = true
        existing.manualDeadline = Date.now() + TIMEOUT
        armDeadline(existing, existing.manualDeadline)
        pumpTasks()
      }
      return existing.promise
    }
    const merged = { ...current.value.get(key)!, ...overrides.value[key] }
    const age = Date.now() - publicProductQuoteTime(merged)
    if (intent !== 'refresh' && age >= 0 && age < SUCCESS_TTL && states.value[key]?.status !== 'failed') {
      return Promise.resolve({ kind: 'success', url })
    }
    const attempt = attempts.get(key)
    if (attempt && Date.now() - attempt.at < ATTEMPT_INTERVAL) {
      return Promise.resolve(attempt.result || { kind: 'failed', url, reason: 'cooldown' })
    }
    if (intent === 'automatic' && Date.now() < pausedUntil.value) return Promise.resolve({ kind: 'cancelled' })
    let resolve!: Task['resolve']
    const promise = new Promise<PublicProductQuoteResult>(done => { resolve = done })
    const task: Task = {
      key, product, promise, resolve, manual: intent !== 'automatic', running: false,
      done: false, requested: false, controller: new AbortController(), deadline: Infinity,
    }
    tasks.set(key, task)
    busy.value.add(key)
    if (task.manual) {
      task.manualDeadline = Date.now() + TIMEOUT
      armDeadline(task, task.manualDeadline)
    }
    pumpTasks()
    return promise
  }

  function cancelAutomatic() {
    // Suppress pumping until all old automatic tasks have been removed.
    const previous = disposed
    disposed = true
    for (const task of [...tasks.values()]) {
      if (!task.manual) {
        delete states.value[task.key]
        finish(task, { kind: 'cancelled' })
      }
    }
    disposed = previous
    pumpTasks()
  }

  function startBatch(page: PublicAccountImportProduct[]) {
    void page
    cancelAutomatic()
  }

  watch(catalog, () => {
    const keys = current.value
    for (const key of Object.keys(overrides.value)) {
      const base = keys.get(key)
      if (!base || publicProductQuoteTime(base) > Date.parse(overrides.value[key].quote_verified_at)) {
        delete overrides.value[key]
        delete states.value[key]
      }
    }
    for (const key of Object.keys(unavailable.value)) {
      const base = keys.get(key)
      if (!base || publicProductQuoteTime(base) > unavailable.value[key]) delete unavailable.value[key]
    }
    for (const key of Object.keys(states.value)) if (!keys.has(key)) delete states.value[key]
    for (const key of attempts.keys()) if (!keys.has(key)) attempts.delete(key)
    for (const task of [...tasks.values()]) if (!keys.has(task.key)) finish(task, { kind: 'cancelled' })
  }, { flush: 'sync' })

  function dispose() {
    disposed = true
    clearInterval(clockTimer)
    for (const task of [...tasks.values()]) finish(task, { kind: 'cancelled' })
  }
  onScopeDispose(dispose)

  return { products, status, refreshDisabled, refreshCooldown, request, startBatch, cancelAutomatic, pausedUntil, dispose }
}
