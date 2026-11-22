const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const { spawnSync } = require('node:child_process')
const { candidateProbeHold } = require('./candidate-probes')
const { SHOP_CANONICAL_ORIGIN } = require('./shop-origins')

const endpoint = new URL('/shopApi/Shop/goodsInfo', SHOP_CANONICAL_ORIGIN)
const idPattern = /^[a-zA-Z0-9_-]{1,100}$/
const publicFields = ['description', 'goods_description', 'goods_desc', 'desc', 'introduce', 'introduction',
  'goods_introduce', 'summary', 'purchase_notice', 'buy_notice', 'buy_tips', 'notice', 'warranty']

function readJSON(file, fallback) {
  try { return JSON.parse(fs.readFileSync(file, 'utf8')) } catch (error) {
    if (error.code === 'ENOENT') return fallback
    throw error
  }
}

function saveJSON(file, value) {
  const temp = file + '.tmp'
  const fd = fs.openSync(temp, 'w', 0o600)
  try { fs.writeFileSync(fd, JSON.stringify(value)); fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
  fs.renameSync(temp, file)
}

// flock attaches to the inherited open file description. The resident process
// holds it across the request and checkpoint, and the OS releases it on exit.
function acquire(file) {
  const fd = fs.openSync(file, 'a', 0o600)
  const lock = spawnSync('flock', ['-n', '3'], { stdio: ['ignore', 'ignore', 'ignore', fd] })
  if (lock.status === 0) return () => fs.closeSync(fd)
  fs.closeSync(fd)
  if (lock.status === 1) return null
  throw lock.error || new Error('detail task lock unavailable')
}

function validateManifest(m) {
  if (m.schema !== 1 || !idPattern.test(m.id) || !/^[a-f0-9]{64}$/.test(m.snapshot_sha256)
    || !Array.isArray(m.products) || !m.products.length || !Number.isSafeInteger(m.resume ?? 0) || (m.resume ?? 0) < 0) throw new Error('invalid detail manifest')
  const seen = new Set()
  for (const p of m.products) {
    const expected = crypto.createHash('sha256').update(p.shop_id + ':' + p.goods_key).digest('hex').slice(0, 24)
    if (!idPattern.test(p.shop_id) || !idPattern.test(p.goods_key) || expected !== p.id || seen.has(p.id)) throw new Error('invalid or duplicate frozen product')
    seen.add(p.id)
  }
}

function extractDescription(payload, row) {
  if (payload?.code !== 1 || !payload.data) return { state: 'missing', reason: '公开商品接口未返回该商品' }
  const data = payload.data
  const goods = data.goods || data.item || data.product || data.goods_info || data
  if (String(goods.goods_key || data.goods_key || '') !== row.goods_key) return { state: 'failed', reason: '详情身份无法与冻结商品核对' }
  const fields = []
  // Only pre-purchase descriptions. Never copy card inventory, paid article
  // bodies, order data, account tokens or the entire upstream response.
  for (const obj of [goods, goods.extend, data]) {
    for (const key of publicFields) {
      const value = obj?.[key]
      if (typeof value === 'string' && value.trim() && !fields.includes(value)) fields.push(value)
    }
  }
  if (!fields.length) return { state: 'missing', reason: '公开响应未提供购买前介绍字段' }
  const original = fields.join('\n\n')
  if (Buffer.byteLength(original) > 128 * 1024) return { state: 'failed', reason: '原文超过单件证据上限' }
  return { state: 'read', original }
}

async function fetchDescription(row, options) {
  const signal = AbortSignal.any([options.signal, AbortSignal.timeout(20_000)])
  const response = await (options.fetch || fetch)(endpoint, {
    method: 'POST', redirect: 'manual', credentials: 'omit', signal,
    body: new URLSearchParams({ goods_key: row.goods_key, trade_no: '' }),
  })
  if ([401, 403, 407].includes(response.status) || (response.status >= 300 && response.status < 400)) {
    await response.body?.cancel()
    return { state: 'blocked', reason: '公开详情入口拒绝匿名读取或要求跳转 (HTTP ' + response.status + ')' }
  }
  if (response.status === 429 || response.status >= 500) {
    await response.body?.cancel()
    const header = response.headers.get('retry-after')
    const retry = Number(header)
    const retryMs = Number.isFinite(retry) ? retry * 1000 : Date.parse(header) - Date.now()
    return { state: 'retry', reason: 'HTTP ' + response.status, retry_ms: Math.max(60_000, retryMs || 0) }
  }
  if (!response.ok) { await response.body?.cancel(); return { state: 'failed', reason: '公开详情 HTTP ' + response.status } }
  const chunks = []
  let length = 0
  for await (const chunk of response.body) {
    length += chunk.length
    if (length > 1024 * 1024) return { state: 'failed', reason: '公开响应超过读取上限' }
    chunks.push(chunk)
  }
  const body = Buffer.concat(chunks).toString('utf8')
  let payload
  try { payload = JSON.parse(body) } catch {
    if (/captcha|challenge|人机验证|访问验证|安全验证|access denied|waf/i.test(body)) return { state: 'blocked', reason: '公开详情入口返回访问验证页' }
    return { state: 'failed', reason: '公开响应不是商品数据' }
  }
  if (payload?.code !== 1 && /验证码|安全验证|登录后|请登录|captcha|access denied|unauthorized/i.test(String(payload?.msg || ''))) {
    return { state: 'blocked', reason: '公开详情入口要求身份验证' }
  }
  return extractDescription(payload, row)
}

class ProductDetails {
  constructor(directory, options = {}) {
    this.directory = directory
    this.lock = options.lock || acquire
    this.save = options.save || saveJSON
    this.now = options.now || Date.now
    this.active = false
    this.nextCheck = 0
    this.manifest = null
  }

  async run(options) {
    if (this.active || this.now() < this.nextCheck) return false
    this.active = true
    this.nextCheck = this.now() + 5000
    let release
    try {
      const file = path.join(this.directory, 'product-details.json')
      if (!fs.existsSync(file)) { this.nextCheck = this.now() + 30_000; return false }
      release = this.lock(path.join(this.directory, 'product-details.lock'))
      if (!release) return false
      const stat = fs.statSync(file, { bigint: true })
      const stamp = [stat.ino, stat.size, stat.mtimeNs].join('/')
      if (stamp !== this.stamp) {
        const m = readJSON(file)
        if (m.enabled === true) validateManifest(m)
        this.manifest = m
        this.stamp = stamp
        this.identities = m.enabled === true ? crypto.createHash('sha256').update(JSON.stringify(m.products)).digest('hex') : ''
      }
      const m = this.manifest
      if (m.enabled !== true) return false
      const dir = path.join(this.directory, 'product-details', m.id)
      fs.mkdirSync(path.join(dir, 'offers'), { recursive: true, mode: 0o700 })
      const checkpoint = path.join(dir, 'status.json')
      const state = readJSON(checkpoint, { id: m.id, snapshot_sha256: m.snapshot_sha256, identities: this.identities,
        total: m.products.length, cursor: 0, resume: m.resume || 0, attempts: 0, counts: {}, state: 'running' })
      if (state.snapshot_sha256 !== m.snapshot_sha256 || state.identities !== this.identities) throw new Error('detail manifest changed during a frozen run')
      if (state.state === 'complete') return false
      if (state.state === 'blocked' && state.resume >= (m.resume || 0)) return false
      if (state.retry_at > this.now()) return false
      if (candidateProbeHold(await options.control(), this.now(), true)) return false
      state.resume = m.resume || 0
      const row = m.products[state.cursor]
      const resultFile = path.join(dir, 'offers', row.id + '.json')
      let result = readJSON(resultFile)
      if (!result) {
        await options.take()
        if (options.signal.aborted) return false
        state.state = 'running'
        state.attempts++
        this.save(checkpoint, state)
        try { result = await (options.read || fetchDescription)(row,options) } catch (error) {
          if (options.signal.aborted) return false
          result = { state: 'retry', reason: '公开详情连接暂不可用', retry_ms: 60_000 }
        }
        if (result.state === 'retry' || result.state === 'blocked') {
          options.pressure?.(result)
          state.failure_streak = (state.failure_streak || 0) + 1
          state.state = result.state === 'blocked' || state.failure_streak >= 3 ? 'blocked' : 'waiting'
          state.retry_at = this.now() + (result.retry_ms || 0)
          state.reason = result.reason
          state.updated_at = new Date(this.now()).toISOString()
          this.save(checkpoint,state)
          return true
        }
        result = { ...result, id: row.id, shop_id: row.shop_id, goods_key: row.goods_key, at: new Date(this.now()).toISOString() }
        this.save(resultFile, result)
      }
      state.counts[result.state] = (state.counts[result.state] || 0) + 1
      state.cursor++
      state.failure_streak = 0
      state.retry_at = 0
      state.reason = ''
      state.state = state.cursor === state.total ? 'complete' : 'running'
      state.updated_at = new Date(this.now()).toISOString()
      this.save(checkpoint, state)
      this.nextCheck = 0
      return true
    } finally { release?.(); this.active = false }
  }
}

module.exports = { ProductDetails, extractDescription, fetchDescription, validateManifest }
