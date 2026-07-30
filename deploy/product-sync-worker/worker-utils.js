const supportedProxyProtocols = new Set(['http:', 'https:', 'socks5:'])
const inventorylessGoodsTypes = new Set(['article', 'resource', 'equity'])

class ShopSyncError extends Error {
  constructor(kind, message) {
    super(message)
    this.name = 'ShopSyncError'
    this.kind = kind
  }
}

class JobLeaseLostError extends Error {
  constructor(message = 'product sync job lease expired', options = {}) {
    super(message)
    this.name = 'JobLeaseLostError'
    this.kind = 'lease_lost'
    this.restartLane = Boolean(options.restartLane)
  }
}

function abortReason(signal) {
  return signal?.reason instanceof Error
    ? signal.reason
    : new Error(String(signal?.reason || 'operation aborted'))
}

function throwIfAborted(signal) {
  if (signal?.aborted) throw abortReason(signal)
}

function waitForPromiseOrAbort(promise, signal) {
  if (!signal) return Promise.resolve(promise)
  if (signal.aborted) return Promise.reject(abortReason(signal))

  return new Promise((resolve, reject) => {
    const onAbort = () => reject(abortReason(signal))
    signal.addEventListener('abort', onAbort, { once: true })
    Promise.resolve(promise).then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error) => {
        signal.removeEventListener('abort', onAbort)
        reject(error)
      }
    )
  })
}

function createJobHeartbeat(options = {}) {
  const send = options.send
  if (typeof send !== 'function') throw new Error('heartbeat sender is required')
  const intervalMilliseconds = parsePositiveMilliseconds(
    options.intervalMilliseconds,
    30_000,
    'heartbeat interval'
  )
  const isLeaseLost = options.isLeaseLost || ((error) => error?.status === 409)
  const onLeaseLost = options.onLeaseLost || (() => {})
  const onError = options.onError || (() => {})
  let stopped = false
  let leaseLost = false
  let pending = Promise.resolve()
  let timer

  const runNow = () => {
    if (stopped) return pending
    pending = pending
      .then(() => {
        if (!stopped) return send()
      })
      .catch(async (error) => {
        if (!isLeaseLost(error)) {
          onError(error)
          return
        }
        stopped = true
        leaseLost = true
        clearInterval(timer)
        try {
          await onLeaseLost(error)
        } catch (callbackError) {
          onError(callbackError)
        }
      })
    return pending
  }

  timer = setInterval(runNow, intervalMilliseconds)
  timer.unref?.()
  return {
    runNow,
    get leaseLost() { return leaseLost },
    async stop() {
      stopped = true
      clearInterval(timer)
      await pending
    },
  }
}

function parsePositiveMilliseconds(value, fallback, name) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    if (value === undefined || value === null || value === '') return fallback
    throw new Error(`${name} must be a positive number`)
  }
  return Math.floor(parsed)
}

function parseSyncConcurrency(value, fallback = 1) {
  if (value === undefined || value === null || value === '') return fallback
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 6) {
    throw new Error('PRODUCT_SYNC_CONCURRENCY must be between 1 and 6')
  }
  return parsed
}

function parseRequestRatePerLane(value, fallback = 1) {
  if (value === undefined || value === null || String(value).trim() === '') return fallback
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed < 0.1 || parsed > 1) {
    throw new Error('PRODUCT_SYNC_REQUEST_RATE_PER_LANE must be between 0.1 and 1')
  }
  return parsed
}

function parseProxyConfiguration(value, name = 'PRODUCT_SYNC_PROXY_URL') {
  const raw = String(value || '').trim()
  if (!raw) return null

  let parsed
  try {
    parsed = new URL(raw)
  } catch {
    throw new Error(`${name} must be a valid URL`)
  }
  if (!supportedProxyProtocols.has(parsed.protocol)) {
    throw new Error(`${name} must use http, https, or socks5`)
  }
  if (!parsed.hostname || (parsed.pathname !== '' && parsed.pathname !== '/') || parsed.search || parsed.hash) {
    throw new Error(`${name} must contain only proxy credentials, host, and port`)
  }
  const username = decodeURIComponent(parsed.username)
  const password = decodeURIComponent(parsed.password)
  if (parsed.protocol === 'socks5:' && (username || password)) {
    throw new Error('authenticated SOCKS5 proxies are not supported; use an HTTP/HTTPS proxy')
  }
  return {
    server: `${parsed.protocol}//${parsed.host}`,
    username,
    password,
  }
}

function parseProxyConfigurations(value, legacyValue, name = 'PRODUCT_SYNC_PROXY_URLS') {
  const raw = String(value || '').trim()
  const entries = raw
    ? raw.split(/[\s,]+/).filter(Boolean)
    : String(legacyValue || '').trim() ? [String(legacyValue).trim()] : []
  if (entries.length > 6) throw new Error(`${name} supports at most 6 proxies`)
  return entries.map((entry, index) => parseProxyConfiguration(entry, `${name} entry ${index + 1}`))
}

function proxyLanesForConcurrency(concurrency, configurations) {
  if (!Array.isArray(configurations)) throw new Error('proxy configurations must be an array')
  if (configurations.length > 0 && configurations.length !== concurrency) {
    throw new Error('PRODUCT_SYNC_PROXY_URLS count must match PRODUCT_SYNC_CONCURRENCY')
  }
  if (concurrency > 1 && configurations.length === 0) {
    throw new Error(`PRODUCT_SYNC_CONCURRENCY=${concurrency} requires ${concurrency} PRODUCT_SYNC_PROXY_URLS entries`)
  }
  const laneKeys = configurations.map((proxy) => `${proxy.server}\0${proxy.username}\0${proxy.password}`)
  if (new Set(laneKeys).size !== laneKeys.length) {
    throw new Error('PRODUCT_SYNC_PROXY_URLS entries must be distinct')
  }
  return configurations.length > 0 ? configurations : [null]
}

function proxyPoolsForConcurrency(concurrency, configurations, fallbackConfigurations = []) {
  const laneProxies = proxyLanesForConcurrency(concurrency, configurations)
  if (!Array.isArray(fallbackConfigurations)) throw new Error('fallback proxy configurations must be an array')
  if (fallbackConfigurations.length > 0 && configurations.length === 0) {
    throw new Error('PRODUCT_SYNC_PROXY_FALLBACK_URLS requires PRODUCT_SYNC_PROXY_URLS')
  }
  if (fallbackConfigurations.length > concurrency) {
    throw new Error('PRODUCT_SYNC_PROXY_FALLBACK_URLS count cannot exceed PRODUCT_SYNC_CONCURRENCY')
  }

  const allProxies = [...configurations, ...fallbackConfigurations]
  const proxyKeys = allProxies.map((proxy) => `${proxy.server}\0${proxy.username}\0${proxy.password}`)
  if (new Set(proxyKeys).size !== proxyKeys.length) {
    throw new Error('all product sync proxy endpoints must be distinct')
  }

  return laneProxies.map((proxy, index) => (
    fallbackConfigurations[index] ? [proxy, fallbackConfigurations[index]] : [proxy]
  ))
}

function isVerificationPageState(state) {
  if (!state) return false
  if (state.hasCaptcha) return true
  const value = `${state.title || ''}\n${state.text || ''}`.toLowerCase()
  return value.includes('verification')
    || value.includes('please slide to verify')
    || value.includes('verify that you are a real person')
    || value.includes('滑动验证')
}

function unavailableMessage(value) {
  const message = String(value || '').toLowerCase()
  return /商品.*(?:不存在|未上架|下架|售罄|删除)/.test(message)
    || /(?:库存|存货).*(?:不足|不够|售罄|无货)/.test(message)
    || /(?:低于|小于).*成本价.*(?:无法|不能|不可)?.*购买/.test(message)
    || /(?:无法|不能|不可).*购买.*(?:库存|成本价)/.test(message)
    || /(?:product|goods).*(?:not found|unavailable|off shelf|sold out|deleted)/.test(message)
    || /(?:insufficient|not enough|out of) stock/.test(message)
    || /(?:below|lower than).*(?:cost|cost price)/.test(message)
}

function shopUnavailableMessage(value) {
  const message = String(value || '').trim().toLowerCase()
  return /(?:店铺|商店)(?:链接)?.*(?:不存在|已关闭|已删除|已注销|已停用)/.test(message)
    || /(?:shop|store).*(?:not found|does not exist|closed|deleted|disabled)/.test(message)
}

function normalizeNonNegativeInteger(value) {
  if (value === null || value === undefined || typeof value === 'boolean'
    || (typeof value === 'string' && value.trim() === '')) return null
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : null
}

function normalizeNonNegativeAmount(value) {
  if (value === null || value === undefined || typeof value === 'boolean'
    || (typeof value === 'string' && value.trim() === '')) return null
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null
}

function normalizeGoodsStatus(value) {
  if (value === true || value === 1 || value === '1') return 'available'
  if (value === false || value === 0 || value === '0') return 'unavailable'
  const normalized = String(value ?? '').trim().toLowerCase()
  if (['active', 'enabled', 'on_sale', 'onsale', 'selling'].includes(normalized)) return 'available'
  if (['inactive', 'disabled', 'off_sale', 'offsale', 'sold_out', 'deleted'].includes(normalized)) return 'unavailable'
  return 'unknown'
}

function catalogProductState(item, fallbackGoodsType) {
  const goodsKey = String(item?.goods_key || '').trim()
  if (!goodsKey || goodsKey.length > 100) {
    return { state: 'unknown', reason: 'catalog item has no valid goods_key' }
  }
  const statusValues = []
  if (Object.prototype.hasOwnProperty.call(item, 'status')) statusValues.push(item.status)
  if (item?.extend && Object.prototype.hasOwnProperty.call(item.extend, 'status')) statusValues.push(item.extend.status)
  if (statusValues.length > 0) {
    const statuses = statusValues.map(normalizeGoodsStatus)
    if (statuses.includes('unknown')) {
      return { state: 'unknown', reason: `catalog item ${goodsKey} has no valid status` }
    }
    if (statuses.includes('unavailable')) return { state: 'unavailable', goodsKey }
  }

  const goodsType = String(item?.goods_type || fallbackGoodsType || '').trim().toLowerCase()
  const rawStock = item?.extend?.stock_count
  const rawMinimumQuantity = item?.extend?.limit_count
  const hasImplicitSingleQuantity = inventorylessGoodsTypes.has(goodsType)
    && rawStock === undefined && rawMinimumQuantity === undefined
  const stock = hasImplicitSingleQuantity ? 1 : normalizeNonNegativeInteger(rawStock)
  const configuredMinimumQuantity = hasImplicitSingleQuantity ? 1 : normalizeNonNegativeInteger(rawMinimumQuantity)
  if (stock === null || configuredMinimumQuantity === null) {
    return { state: 'unknown', reason: `catalog item ${goodsKey} has invalid stock or minimum quantity` }
  }
  const minimumQuantity = Math.max(configuredMinimumQuantity, 1)
  if (stock < minimumQuantity) return { state: 'unavailable', goodsKey }

  const name = String(item?.name || '').trim()
  const url = String(item?.link || '').trim()
  const price = normalizeNonNegativeAmount(item?.price)
  const marketPrice = item?.market_price === undefined || item?.market_price === null || item?.market_price === ''
    ? 0
    : normalizeNonNegativeAmount(item.market_price)
  if (!name || !url || !['card', 'article', 'resource', 'equity'].includes(goodsType) || price === null || marketPrice === null) {
    return { state: 'unknown', reason: `catalog item ${goodsKey} is missing required fields` }
  }

  return {
    state: 'candidate',
    goodsKey,
    product: {
      goods_key: goodsKey,
      name,
      url,
      image: String(item?.image || '').trim(),
      category: String(item?.category?.name || '').trim(),
      goods_type: goodsType,
      price,
      market_price: marketPrice,
      stock,
      minimum_quantity: minimumQuantity,
    },
  }
}

function channelIsValid(channel) {
  if (!channel || typeof channel !== 'object') return false
  const id = channel.id
  if ((typeof id !== 'string' && typeof id !== 'number') || String(id).trim() === '') return false
  if (channel.enabled === false || channel.disabled === true) return false
  if (channel.status !== undefined && normalizeGoodsStatus(channel.status) !== 'available') return false
  return true
}

function channelIsDefault(channel) {
  return channel.is_default === true || channel.is_default === 1 || channel.is_default === '1'
    || channel.default === true || channel.default === 1 || channel.default === '1'
    || channel.isDefault === true || channel.selected === true
}

function selectPaymentChannel(channels) {
  if (!Array.isArray(channels)) return null
  const valid = channels.filter(channelIsValid)
  return valid.find(channelIsDefault) || valid[0] || null
}

function quoteResult(payload) {
  if (!payload || typeof payload !== 'object') return { state: 'unknown', reason: 'quote response is invalid' }
  if (payload.code !== 1) {
    return unavailableMessage(payload.msg)
      ? { state: 'unavailable' }
      : { state: 'unknown', reason: String(payload.msg || 'quote request failed') }
  }
  if (!payload.data || typeof payload.data !== 'object') {
    return { state: 'unknown', reason: 'quote response has no data' }
  }
  if (payload.data.status !== undefined) {
    const status = normalizeGoodsStatus(payload.data.status)
    if (status === 'unavailable') return { state: 'unavailable' }
    if (status === 'unknown') return { state: 'unknown', reason: 'quote response has an invalid status' }
  }
  const totalAmount = normalizeNonNegativeAmount(payload.data.total_amount)
  return totalAmount === null
    ? { state: 'unknown', reason: 'quote response has no valid total_amount' }
    : { state: 'available', totalAmount }
}

function parseShopHTTPResponse(result) {
  if (!result || typeof result !== 'object') throw new ShopSyncError('unknown', 'shop API returned no response')
  if (result.status === 429) throw new ShopSyncError('rate_limit', 'shop API returned HTTP 429')
  if (result.status === 502 || result.status === 520) {
    throw new ShopSyncError('network', `shop API returned HTTP ${result.status}`)
  }
  const contentType = String(result.contentType || '').toLowerCase()
  if (!contentType.includes('application/json')) {
    throw new ShopSyncError('verification', `shop API verification required: HTTP ${result.status || 0}`)
  }
  if (!Number.isInteger(result.status) || result.status < 200 || result.status >= 300) {
    throw new ShopSyncError('unknown', `shop API returned HTTP ${result.status || 0}`)
  }
  if (!result.payload || typeof result.payload !== 'object') {
    throw new ShopSyncError('unknown', 'shop API returned invalid JSON')
  }
  return result.payload
}

function shopRequestError(path, error) {
  if (error instanceof ShopSyncError) return error
  const message = String(error?.message || error).replace(/[\r\n]+/g, ' ').slice(0, 500)
  const name = String(error?.name || '').toLowerCase()
  const browserSessionClosed = /target (?:page, )?context or browser has been closed|page, context or browser has been closed|browser has been closed|context has been closed|page has been closed/i.test(message)
  const isNetworkFailure = name === 'aborterror'
    || browserSessionClosed
    || /aborterror|signal is aborted|failed to fetch|fetch failed|networkerror|network request failed|load failed|net::err_/i.test(message)
  const wrapped = new ShopSyncError(
    isNetworkFailure ? 'network' : 'unknown',
    `shop API ${path} failed: ${message}`
  )
  if (browserSessionClosed) wrapped.restartLane = true
  return wrapped
}

function isPressureError(error) {
  if (error?.restartLane || error?.kind === 'lease_lost') return false
  return error?.kind === 'rate_limit' || error?.kind === 'verification' || error?.kind === 'network'
}

function pressureBackoffMilliseconds(failureCount, random = Math.random) {
  const delays = [60_000, 300_000, 900_000]
  const base = delays[Math.min(Math.max(1, failureCount), delays.length) - 1]
  const jitter = 0.9 + Math.max(0, Math.min(1, Number(random()))) * 0.2
  return Math.round(base * jitter)
}

async function withPressureRecovery(task, options = {}) {
  if (typeof task !== 'function') throw new Error('pressure recovery task is required')
  const state = options.state || { failureCount: 0 }
  const now = options.now || Date.now
  const sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
  const random = options.random || Math.random
  const deadlineAt = Number.isFinite(options.deadlineAt) ? options.deadlineAt : Number.POSITIVE_INFINITY
  const signal = options.signal

  while (true) {
    throwIfAborted(signal)
    try {
      return await task()
    } catch (error) {
      if (!isPressureError(error)) throw error
      state.failureCount = Math.max(0, Number(state.failureCount) || 0) + 1
      const waitMilliseconds = pressureBackoffMilliseconds(state.failureCount, random)
      if (now() + waitMilliseconds >= deadlineAt) {
        throw new ShopSyncError('unknown', 'product sync job cannot recover from upstream pressure before its deadline')
      }
      await options.onBackoff?.({ error, failureCount: state.failureCount, waitMilliseconds })
      await waitForPromiseOrAbort(sleep(waitMilliseconds), signal)
      throwIfAborted(signal)
      await options.recover?.({ error, failureCount: state.failureCount, waitMilliseconds })
    }
  }
}

class TokenBucket {
  constructor(ratePerSecond, capacity, options = {}) {
    if (!(ratePerSecond > 0) || !(capacity > 0)) throw new Error('token bucket limits must be positive')
    this.ratePerMillisecond = ratePerSecond / 1000
    this.capacity = capacity
    this.tokens = capacity
    this.now = options.now || Date.now
    this.sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
    this.updatedAt = this.now()
    this.pending = Promise.resolve()
  }

  take(signal) {
    const request = this.pending.then(() => this.takeNext(signal))
    this.pending = request.catch(() => {})
    return request
  }

  async takeNext(signal) {
    while (true) {
      throwIfAborted(signal)
      const now = this.now()
      this.tokens = Math.min(this.capacity, this.tokens + Math.max(0, now - this.updatedAt) * this.ratePerMillisecond)
      this.updatedAt = now
      if (this.tokens >= 1) {
        this.tokens -= 1
        return now
      }
      await waitForPromiseOrAbort(
        this.sleep(Math.max(1, Math.ceil((1 - this.tokens) / this.ratePerMillisecond))),
        signal
      )
    }
  }
}

async function takeRequestTokens(laneLimiter, globalLimiter, signal) {
  if (!laneLimiter || typeof laneLimiter.take !== 'function') throw new Error('lane request limiter is required')
  if (!globalLimiter || typeof globalLimiter.take !== 'function') throw new Error('global request limiter is required')
  await laneLimiter.take(signal)
  await globalLimiter.take(signal)
}

async function initializeProxyPool(proxyPool, startIndex, initialize, options = {}) {
  if (!Array.isArray(proxyPool) || proxyPool.length === 0) throw new Error('lane proxy pool is required')
  if (typeof initialize !== 'function') throw new Error('lane initializer is required')
  const attemptsPerProxy = Number.isInteger(options.attemptsPerProxy) && options.attemptsPerProxy > 0
    ? options.attemptsPerProxy
    : 2
  const sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
  const retryMilliseconds = Number.isFinite(options.retryMilliseconds)
    ? Math.max(0, options.retryMilliseconds)
    : 2_000
  const normalizedStartIndex = Number.isInteger(startIndex)
    ? ((startIndex % proxyPool.length) + proxyPool.length) % proxyPool.length
    : 0
  let lastError

  for (let offset = 0; offset < proxyPool.length; offset += 1) {
    const proxyIndex = (normalizedStartIndex + offset) % proxyPool.length
    for (let attempt = 1; attempt <= attemptsPerProxy; attempt += 1) {
      try {
        return {
          value: await initialize(proxyPool[proxyIndex], proxyIndex, attempt),
          proxyIndex,
          attempt,
        }
      } catch (error) {
        lastError = error
        await options.onFailure?.({ error, proxy: proxyPool[proxyIndex], proxyIndex, attempt })
        const isLastAttempt = offset === proxyPool.length - 1 && attempt === attemptsPerProxy
        if (!isLastAttempt && retryMilliseconds > 0) await sleep(retryMilliseconds)
      }
    }
  }
  throw lastError || new Error('lane initialization failed')
}

async function closeContextThenCreate(previousContext, createContext) {
  if (typeof createContext !== 'function') throw new Error('replacement context factory is required')
  await previousContext?.close().catch(() => {})
  return createContext()
}

function browserResourceCounts(contexts) {
  const activeContexts = Array.isArray(contexts) ? contexts.filter(Boolean) : []
  let pageCount = 0
  for (const context of activeContexts) {
    try {
      const pages = context.pages()
      pageCount += Array.isArray(pages)
        ? pages.filter((page) => typeof page?.isClosed !== 'function' || !page.isClosed()).length
        : 0
    } catch {
      // A context can disappear while status is being collected.
    }
  }
  return { contextCount: activeContexts.length, pageCount }
}

function workerStatusIsHealthy(status, now = Date.now(), maxStaleMilliseconds = 120_000) {
  if (!status || typeof status !== 'object') return false
  if (['error', 'stopped', 'stopping'].includes(status.state)) return false
  const updatedAt = Date.parse(status.updated_at)
  if (!Number.isFinite(updatedAt) || updatedAt > now + 60_000 || now - updatedAt > maxStaleMilliseconds) return false
  if (!Number.isInteger(status.browser_context_count) || status.browser_context_count < 1) return false
  if (!Number.isInteger(status.browser_page_count) || status.browser_page_count < 1) return false
  if (!Array.isArray(status.lanes) || !status.lanes.some((lane) => (
    lane?.context_ready === true && ['idle', 'syncing', 'blocked'].includes(lane.state)
  ))) {
    return false
  }
  return true
}

class Semaphore {
  constructor(limit) {
    if (!Number.isInteger(limit) || limit < 1) throw new Error('semaphore limit must be a positive integer')
    this.limit = limit
    this.active = 0
    this.queue = []
  }

  async acquire() {
    if (this.active < this.limit) {
      this.active += 1
      return
    }
    await new Promise((resolve) => this.queue.push(resolve))
  }

  release() {
    const next = this.queue.shift()
    if (next) next()
    else this.active -= 1
  }

  async run(task) {
    await this.acquire()
    try {
      return await task()
    } finally {
      this.release()
    }
  }
}

async function mapWithConcurrency(items, concurrency, mapper) {
  const results = new Array(items.length)
  let nextIndex = 0
  let firstError
  const workers = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (!firstError) {
      const index = nextIndex
      nextIndex += 1
      if (index >= items.length) return
      try {
        results[index] = await mapper(items[index], index)
      } catch (error) {
        firstError = error
      }
    }
  })
  await Promise.all(workers)
  if (firstError) throw firstError
  return results
}

function simulatedTokenBucketDuration(requestCount, ratePerSecond = 1, capacity = 1) {
  if (!Number.isInteger(requestCount) || requestCount <= 0) return 0
  return Math.max(0, requestCount - capacity) / ratePerSecond * 1000
}

module.exports = {
  JobLeaseLostError,
  Semaphore,
  ShopSyncError,
  TokenBucket,
  browserResourceCounts,
  catalogProductState,
  closeContextThenCreate,
  createJobHeartbeat,
  initializeProxyPool,
  isPressureError,
  isVerificationPageState,
  mapWithConcurrency,
  normalizeNonNegativeInteger,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseProxyConfigurations,
  parseRequestRatePerLane,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  proxyLanesForConcurrency,
  proxyPoolsForConcurrency,
  quoteResult,
  selectPaymentChannel,
  shopRequestError,
  shopUnavailableMessage,
  simulatedTokenBucketDuration,
  takeRequestTokens,
  throwIfAborted,
  unavailableMessage,
  waitForPromiseOrAbort,
  withPressureRecovery,
  workerStatusIsHealthy,
}
