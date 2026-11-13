const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const { collectCatalog } = require('./catalog-sync')
const { shopUnavailableMessage, shopClosedForTransactionsMessage } = require('./worker-utils')

function candidateProbeHold(data, now = Date.now(), allowBusy = false) {
  const state = data?.adaptive
  const worker = data?.worker
  const sample = state?.history?.at(-1)
  const fresh = (value) => Number.isFinite(Date.parse(value)) && now - Date.parse(value) <= 120_000 && Date.parse(value) <= now + 10_000
  if (data?.control?.mode !== 'adaptive' || data.control.automatic_paused || state?.automatic_hold) return 'automatic collection paused'
  if (!sample || !fresh(sample.at) || !fresh(worker?.updated_at)) return 'resource or Worker observation stale'
  if (sample.pressure || worker.global_pressure_state === 'pressured'
    || Date.parse(worker.global_pressure_until) > now || Date.parse(worker.global_silence_until) > now) return 'upstream pressure'
  if (worker.available_lane_count < 5 || !(sample.worker_memory_ratio < .88)
    || !(sample.host_available_ratio >= .2) || !(sample.cpu_ratio <= .85) || !(sample.disk_free_bytes >= 1073741824)) return 'waiting for healthy resources'
  if (!allowBusy && (sample.due > 0 || data.automatic_eligible > 0)) return 'existing collection queue has priority'
  return ''
}

async function collectCandidateProbe(token, options) {
  let info
  const snapshot = await (options.collect || collectCatalog)({
    shopToken: token, signal: options.signal,
    post: async (requestPath, body) => {
      const payload = await options.post(requestPath, body)
      if (requestPath === '/shopApi/Shop/info') info = payload
      return payload
    },
  })
  if (!snapshot.complete || !Array.isArray(snapshot.products) || snapshot.source_product_count !== snapshot.products.length) throw new Error('candidate catalog is incomplete')
  const data = info?.data || {}
  const shop = data.shop || data.shop_info || data
  const name = [shop.name, shop.shop_name, data.shop_name, shop.title].find((value) => typeof value === 'string' && value.trim()) || ''
  const effective = snapshot.products.filter((item) => item.sellable).length
  const unavailable = shopUnavailableMessage(info?.msg)
  const missing = unavailable && /不存在|已删除|已注销|not found|does not exist|deleted/i.test(String(info?.msg || ''))
  const state = snapshot.closed
    ? missing ? 'not_found' : (unavailable || shopClosedForTransactionsMessage(info?.msg)) ? 'closed' : 'unconfirmed'
    : effective > 0 ? 'available' : snapshot.source_product_count > 0 ? 'unavailable' : 'empty'
  return { state, name: name.trim().slice(0, 80), source_count: snapshot.source_product_count,
    effective_count: effective, message: String(info?.msg || '').slice(0, 200), catalog: snapshot }
}

class CandidateProbes {
  constructor(directory, options = {}) {
    this.queueFile = path.join(directory, 'candidate-probes.json')
    this.resultFile = path.join(directory, 'candidate-probe-results.json')
    this.active = false
    this.nextCheck = 0
    this.now = options.now || Date.now
    this.write = options.write || ((file, value) => {
      const temporary = `${file}.tmp`
      fs.writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
      fs.renameSync(temporary, file)
    })
  }

  manifest() {
    let input
    try { input = JSON.parse(fs.readFileSync(this.queueFile, 'utf8')) } catch (error) {
      if (error.code === 'ENOENT') return null
      throw error
    }
    if (input.enabled !== true) return null
    if (input.version !== 1 || typeof input.id !== 'string' || !/^[A-Za-z0-9_-]{1,80}$/.test(input.id)
      || !Number.isFinite(Date.parse(input.expires_at))
      || !Array.isArray(input.candidates) || input.candidates.length > 100) throw new Error('invalid or expired candidate probe manifest')
    if (Date.parse(input.expires_at) <= this.now()) return null
    const tokens = input.candidates.map((row) => row.platform_id)
    if (tokens.some((token) => typeof token !== 'string' || !/^[A-Za-z0-9_-]{1,100}$/.test(token)) || new Set(tokens).size !== tokens.length) throw new Error('invalid or duplicate candidate identity')
    return input
  }

  async run(options) {
    if (this.active || this.now() < this.nextCheck) return false
    this.active = true
    this.nextCheck = this.now() + 10_000
    try {
      const manifest = this.manifest()
      if (!manifest) return false
      let state
      try { state = JSON.parse(fs.readFileSync(this.resultFile, 'utf8')) } catch (error) {
        if (error.code !== 'ENOENT') throw error
      }
      const identities = crypto.createHash('sha256').update(JSON.stringify(manifest.candidates.map((row) => row.platform_id))).digest('hex')
      if (state && (state.id !== manifest.id || state.identities_sha256 !== identities)) throw new Error('archive previous probe results before replacing the manifest')
      state ||= { version: 1, id: manifest.id, source_sha256: manifest.source_sha256, identities_sha256: identities, results: {}, created_at: new Date(this.now()).toISOString() }
      const row = manifest.candidates.find((item) => {
        const previous = state.results[item.platform_id]
        return !previous || previous.state === 'running' || (previous.state === 'waiting' && Date.parse(previous.retry_at) <= this.now())
      })
      if (!row) return false
      if (candidateProbeHold(await options.control(), this.now())) return false
      const token = row.platform_id
      const attempts = (state.results[token]?.attempts || 0) + 1
      const started = new Date(this.now()).toISOString()
      state.results[token] = { state: 'running', platform_id: token, attempts, started_at: started }
      // Do not start upstream work unless the attempt is durable.
      this.write(this.resultFile, state)
      let result
      let failure
      try {
        result = await options.execute(row)
      } catch (error) {
        failure = error
        const waiting = error.kind === 'probe_paused' || error.candidateProbeInterrupted
        result = { state: waiting ? 'waiting' : 'failed', kind: String(error.kind || 'unknown'), error: options.errorMessage(error),
          ...(waiting ? { retry_at: new Date(this.now() + 60_000).toISOString() } : {}), ...(error.cost ? { cost: error.cost } : {}) }
      }
      const next = { ...state, results: { ...state.results, [token]: {
        ...result, platform_id: token, attempts, started_at: started, finished_at: new Date(this.now()).toISOString(),
      } }, updated_at: new Date(this.now()).toISOString() }
      next.completed = manifest.candidates.filter((item) => next.results[item.platform_id] && !['running', 'waiting'].includes(next.results[item.platform_id].state)).length
      next.total = manifest.candidates.length
      this.write(this.resultFile, next)
      if (failure?.restartLane || failure?.restartBrowser) throw failure
      return true
    } finally {
      this.active = false
    }
  }
}

module.exports = { CandidateProbes, candidateProbeHold, collectCandidateProbe }
