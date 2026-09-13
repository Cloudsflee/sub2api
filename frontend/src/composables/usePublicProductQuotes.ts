import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import {
  livePublicProductAvailability,
  livePublicProductMinimumQuantity,
  livePublicProductQuoteAvailability,
  parseLivePublicProductQuote,
  publicProductGoodsKey,
  publicProductHref,
  publicProductQuoteTime,
  selectLivePublicProductPaymentChannel,
  type PublicProductQuote,
} from '@/utils/publicProductCatalog'
import { PUBLIC_SHOP_CANONICAL_ORIGIN } from '@/utils/publicShopProductSync'

const SUCCESS_TTL = 60_000
const ATTEMPT_INTERVAL = 15_000
const TIMEOUT = 8_000
const HTTP_INTERVAL = 667

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
  deadline: number
  manualDeadline?: number
  promise: Promise<PublicProductQuoteResult>
  resolve: (result: PublicProductQuoteResult) => void
}
interface HTTPJob {
  task: Task
  start: () => void
}

function identity(product: PublicAccountImportProduct): string {
  return JSON.stringify([product.id, product.shop_id, publicProductGoodsKey(product.url) || product.url])
}

class PressureError extends Error {
  constructor(message: string, readonly delay = SUCCESS_TTL) { super(message) }
}

function visitorID(): string {
  const key = 'sub2api-public-product-visitor'
  try {
    const existing = localStorage.getItem(key)
    if (existing) return existing
    const value = (globalThis.crypto?.randomUUID?.() || `${Date.now()}${Math.random()}`)
      .replace(/[^a-zA-Z0-9]/g, '').slice(0, 32)
    localStorage.setItem(key, value)
    return value
  } catch {
    return 'sub2apipubliccatalog'
  }
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
  const httpQueue: HTTPJob[] = []
  const current = computed(() => new Map(catalog.value.map(product => [identity(product), product])))
  let running = 0
  let disposed = false
  let lastHTTPStart = -Infinity
  let httpTimer: ReturnType<typeof setTimeout> | undefined
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

  function finish(task: Task, result: PublicProductQuoteResult, quote?: PublicProductQuote) {
    if (task.done) return
    task.done = true
    clearTimeout(task.timer)
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

  function pumpHTTP() {
    clearTimeout(httpTimer)
    httpTimer = undefined
    if (disposed || !httpQueue.length) return
    const wait = lastHTTPStart + HTTP_INTERVAL - Date.now()
    if (wait > 0) {
      httpTimer = setTimeout(pumpHTTP, wait)
      return
    }
    const eligible = (job: HTTPJob) => !job.task.done && current.value.has(job.task.key)
      && (job.task.manual || Date.now() >= pausedUntil.value)
    const manual = httpQueue.findIndex(job => job.task.manual && eligible(job))
    const index = manual < 0 ? httpQueue.findIndex(eligible) : manual
    if (index < 0) return
    const job = httpQueue.splice(index, 1)[0]
    if (!job.task.done) {
      lastHTTPStart = Date.now()
      if (!job.task.requested) {
        job.task.requested = true
        attempts.set(job.task.key, { at: Date.now() })
        states.value[job.task.key] = { status: 'checking', at: Date.now() }
      }
      job.start()
    }
    if (httpQueue.length) pumpHTTP()
  }

  function post(task: Task, path: string, payload: Record<string, unknown>): Promise<any> {
    return new Promise((resolve, reject) => {
      const signal = task.controller.signal
      const abort = () => {
        const index = httpQueue.indexOf(job)
        if (index >= 0) httpQueue.splice(index, 1)
        signal.removeEventListener('abort', abort)
        reject(new DOMException('Cancelled', 'AbortError'))
        pumpHTTP()
      }
      const job: HTTPJob = { task, start: () => {
        void (async () => {
          let response: Response
          try {
            response = await fetch(`${PUBLIC_SHOP_CANONICAL_ORIGIN}${path}`, {
              method: 'POST', mode: 'cors', credentials: 'omit', signal,
              headers: { 'Content-Type': 'application/json', Accept: 'application/json', Visitorid: visitorID() },
              body: JSON.stringify(payload),
            })
          } catch (error) {
            if (signal.aborted) throw error
            throw new PressureError('connection')
          }
          if (response.status === 403 || response.status === 429) {
            const retry = response.status === 429 ? response.headers.get('Retry-After') : null
            const delay = retry && /^\d+(\.\d+)?$/.test(retry.trim())
              ? Number(retry) * 1_000 : Date.parse(retry || '') - Date.now()
            throw new PressureError(`HTTP ${response.status}`, Math.max(SUCCESS_TTL, Number.isFinite(delay) ? delay : 0))
          }
          if (!response.ok) throw new Error(`HTTP ${response.status}`)
          if (!response.headers.get('content-type')?.includes('application/json')) throw new Error('non-json')
          return response.json()
        })().then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
      } }
      if (signal.aborted) { abort(); return }
      signal.addEventListener('abort', abort, { once: true })
      httpQueue.push(job)
      pumpHTTP()
    })
  }

  async function execute(task: Task) {
    const url = publicProductHref(task.product.url)
    try {
      const detail = await post(task, '/shopApi/Shop/goodsInfo', {
        goods_key: publicProductGoodsKey(url), trade_no: null,
      })
      if (task.done) return
      const availability = livePublicProductAvailability(detail)
      if (availability === 'unavailable') { finish(task, { kind: 'unavailable' }); return }
      if (availability !== 'available') throw new Error('invalid-details')
      const minimumQuantity = livePublicProductMinimumQuantity(detail.data)
      const token = String(detail.data?.user?.token || '').trim()
      if (!minimumQuantity || !token) throw new Error('invalid-details')
      const channels = await post(task, '/shopApi/Shop/getUserChannel', { token })
      if (task.done) return
      const channel = channels?.code === 1 ? selectLivePublicProductPaymentChannel(channels.data) : null
      if (!channel) throw new Error('invalid-channels')
      const response = await post(task, '/shopApi/Shop/getGoodsPrice', {
        goods_key: publicProductGoodsKey(url), quantity: minimumQuantity, coupon_code: '', channel_id: channel.id,
      })
      if (task.done) return
      if (livePublicProductQuoteAvailability(response) === 'unavailable') {
        finish(task, { kind: 'unavailable' }); return
      }
      const quote = parseLivePublicProductQuote(detail.data, response, Date.now())
      if (!quote) throw new Error('invalid-quote')
      // Older APIs omit goods_type for card products.
      if (!quote.goods_type) quote.goods_type = task.product.goods_type
      finish(task, { kind: 'success', url }, quote)
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
        pumpHTTP()
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
    pumpHTTP()
  }

  function startBatch(page: PublicAccountImportProduct[]) {
    cancelAutomatic()
    if (disposed || Date.now() < pausedUntil.value) return
    for (const product of page.slice(0, 10)) void request(product)
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
    clearTimeout(httpTimer)
    for (const task of [...tasks.values()]) finish(task, { kind: 'cancelled' })
    httpQueue.length = 0
  }
  onScopeDispose(dispose)

  return { products, status, refreshDisabled, request, startBatch, cancelAutomatic, pausedUntil, dispose }
}
