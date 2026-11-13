const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')
const { CandidateProbes, candidateProbeHold, collectCandidateProbe } = require('./candidate-probes')
const { runCandidateProbe } = require('./index')

function healthy(now = Date.now()) {
  return { control: { mode: 'adaptive', automatic_paused: false }, automatic_eligible: 0,
    worker: { updated_at: new Date(now).toISOString(), available_lane_count: 6, global_pressure_state: 'clear' },
    adaptive: { history: [{ at: new Date(now).toISOString(), due: 0, pressure: false,
      worker_memory_ratio: .7, host_available_ratio: .4, cpu_ratio: .2, disk_free_bytes: 2 ** 31 }] } }
}

function fixture(t, tokens = ['one', 'two'], now = Date.now()) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'candidate-probes-'))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  fs.writeFileSync(path.join(directory, 'candidate-probes.json'), JSON.stringify({ version: 1, id: 'test', enabled: true,
    expires_at: new Date(now + 86400_000).toISOString(), candidates: tokens.map((platform_id) => ({ platform_id })) }))
  return directory
}

test('candidate probes wait for healthy observations and give existing jobs priority', () => {
  const now = Date.now()
  assert.equal(candidateProbeHold(healthy(now), now), '')
  for (const change of [
    (v) => { v.control.automatic_paused = true },
    (v) => { v.adaptive.automatic_hold = 'new OOM' },
    (v) => { v.adaptive.history[0].at = new Date(now - 121_000).toISOString() },
    (v) => { v.worker.updated_at = '' },
    (v) => { v.worker.available_lane_count = 4 },
    (v) => { v.adaptive.history[0].pressure = true },
    (v) => { v.worker.global_silence_until = new Date(now + 60_000).toISOString() },
    (v) => { v.adaptive.history[0].worker_memory_ratio = .9 },
    (v) => { v.adaptive.history[0].host_available_ratio = .1 },
    (v) => { v.adaptive.history[0].disk_free_bytes = 500 },
    (v) => { v.automatic_eligible = 1 },
  ]) {
    const data = healthy(now)
    change(data)
    assert.ok(candidateProbeHold(data, now))
  }
})

test('one probe at a time, durable completion and restart resume without repeating completed shops', async (t) => {
  const now = Date.now()
  const directory = fixture(t, ['one', 'two'], now)
  const probes = new CandidateProbes(directory, { now: () => now })
  let release
  let executions = 0
  const options = { control: async () => healthy(now), errorMessage: (e) => e.message,
    execute: async () => { executions++; await new Promise((resolve) => { release = resolve }); return { state: 'empty' } } }
  const first = probes.run(options)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(await probes.run(options), false)
  assert.equal(executions, 1)
  release()
  await first
  let state = JSON.parse(fs.readFileSync(probes.resultFile))
  state.results.two = { state: 'running', attempts: 1 }
  fs.writeFileSync(probes.resultFile, JSON.stringify(state))
  const restarted = new CandidateProbes(directory, { now: () => now + 20_000 })
  await restarted.run({ ...options, control: async () => healthy(now + 20_000), execute: async (row) => {
    assert.equal(row.platform_id, 'two')
    return { state: 'available', source_count: 1, effective_count: 1 }
  } })
  state = JSON.parse(fs.readFileSync(probes.resultFile))
  assert.equal(state.completed, 2)
  assert.equal(state.results.one.attempts, 1)
  assert.equal(state.results.two.attempts, 2)
})

test('invalid identities and failed persistence cannot initiate upstream work', async (t) => {
  for (const tokens of [['one', 'one'], ['https://untrusted.example/path']]) {
    const probes = new CandidateProbes(fixture(t, tokens))
    await assert.rejects(probes.run({}), /identity/)
  }
  let calls = 0
  const probes = new CandidateProbes(fixture(t), { write: () => { throw new Error('disk failed') } })
  await assert.rejects(probes.run({ control: async () => healthy(), execute: async () => { calls++ } }), /disk failed/)
  assert.equal(calls, 0)
  assert.equal(probes.active, false)
})

test('disabled or expired manifests cannot start probes or disturb existing tasks', async (t) => {
  const now = Date.now()
  for (const change of [{ enabled: 'true' }, { enabled: false }, { expires_at: new Date(now - 1).toISOString() }]) {
    const directory = fixture(t, ['one'], now)
    const file = path.join(directory, 'candidate-probes.json')
    fs.writeFileSync(file, JSON.stringify({ ...JSON.parse(fs.readFileSync(file)), ...change }))
    assert.equal(await new CandidateProbes(directory, { now: () => now }).run({ control: () => { throw new Error('must not poll') } }), false)
  }
})

test('pressure waits are resumable and partial or network failures never become not-found', async (t) => {
  const now = Date.now()
  const directory = fixture(t, ['one', 'two'], now)
  const probes = new CandidateProbes(directory, { now: () => now })
  const options = { control: async () => healthy(now), errorMessage: (e) => e.message,
    execute: async () => { throw Object.assign(new Error('waiting for resources'), { kind: 'probe_paused' }) } }
  await probes.run(options)
  let state = JSON.parse(fs.readFileSync(probes.resultFile))
  assert.equal(state.completed, 0)
  assert.equal(state.results.one.state, 'waiting')
  const restarted = new CandidateProbes(directory, { now: () => now + 20_000 })
  await restarted.run({ ...options, execute: async (row) => {
    assert.equal(row.platform_id, 'two')
    throw Object.assign(new Error('network timeout'), { kind: 'network' })
  } })
  state = JSON.parse(fs.readFileSync(probes.resultFile))
  assert.equal(state.results.two.state, 'failed')
  assert.equal(state.completed, 1)
  assert.ok(!state.results.two.catalog)
})

test('candidate catalog uses complete catalog validation and distinguishes confirmed absence', async () => {
  const collected = await collectCandidateProbe('one', { post: async (requestPath) => {
    if (requestPath.endsWith('/info')) return { code: 1, data: { name: 'Example shop' } }
    return { code: 1, data: { total: 0, list: [] } }
  } })
  assert.equal(collected.state, 'empty')
  assert.equal(collected.name, 'Example shop')
  const absent = await collectCandidateProbe('two', { post: async () => ({ code: 0, msg: '店铺不存在' }) })
  assert.equal(absent.state, 'not_found')
  for (const msg of ['店铺已关闭', '商家已被关闭交易']) {
    const closed = await collectCandidateProbe('two', { post: async () => ({ code: 0, msg }) })
    assert.equal(closed.state, 'closed')
  }
  await assert.rejects(collectCandidateProbe('one', { post: async (requestPath) => requestPath.endsWith('/info')
    ? { code: 1, data: {} } : { code: 1, data: { total: 1, list: [] } } }), /incomplete/)
})

test('idle Worker probe uses the existing automatic/probe request budget and read-only control', async (t) => {
  const directory = fixture(t, ['one'])
  const lane = { index: 0 }
  let posts = 0
  await runCandidateProbe(lane, {
    probes: new CandidateProbes(directory),
    backend: async (requestPath) => {
      assert.equal(requestPath, '/api/v1/public/account-import/products/sync-control')
      return healthy()
    },
    publishLaneStatus: () => {},
    collectCandidateProbe: async (token, options) => {
      await options.post('/shopApi/Shop/info', { token })
      return { state: 'not_found' }
    },
    post: async (currentLane, token, requestPath, body) => {
      assert.equal(currentLane, lane)
      assert.equal(lane.syncCost.automatic, true)
      assert.equal(lane.syncCost.probe, true)
      assert.equal(token, 'one')
      assert.deepEqual(body, { token: 'one' })
      lane.syncCost.requests++
      posts++
      return { code: 0, msg: '店铺不存在' }
    },
  })
  assert.equal(posts, 1)
  assert.equal(lane.syncCost, null)
  const saved = JSON.parse(fs.readFileSync(path.join(directory, 'candidate-probe-results.json')))
  assert.equal(saved.results.one.cost.requests, 1)
})
