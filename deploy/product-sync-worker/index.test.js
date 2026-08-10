const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')
const { once } = require('node:events')
const { spawn } = require('node:child_process')

const {
  browserProfileIsInUse,
  clearStaleBrowserProfileLocks,
  challengeStatusValues,
  backend,
  evaluateShopRequest,
  evaluateShopRequestInBrowser,
  laneRecoveryCancellationFailure,
  pressureRecoveryFailure,
  syncJobFinalError,
} = require('./index')

test('stale Firefox profile locks are removed when no browser owns the profile', (t) => {
  if (process.platform !== 'linux') {
    t.skip('Firefox profile process inspection is only available in the Linux worker')
    return
  }
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'sub2api-profile-lock-'))
  const profile = path.join(directory, 'profile')
  fs.mkdirSync(profile)
  fs.writeFileSync(path.join(profile, 'lock'), '')
  fs.writeFileSync(path.join(profile, '.parentlock'), '')
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))

  assert.equal(browserProfileIsInUse(profile), false)
  assert.doesNotThrow(() => clearStaleBrowserProfileLocks(profile))
  assert.equal(fs.existsSync(path.join(profile, 'lock')), false)
  assert.equal(fs.existsSync(path.join(profile, '.parentlock')), false)
})

test('active Firefox profile owner prevents stale lock cleanup', async (t) => {
  if (process.platform !== 'linux') {
    t.skip('Firefox profile process inspection is only available in the Linux worker')
    return
  }
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'sub2api-profile-owner-'))
  const profile = path.join(directory, 'profile')
  fs.mkdirSync(profile)
  fs.writeFileSync(path.join(profile, 'lock'), '')
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))

  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 5000)', '-profile', profile], { stdio: 'ignore' })
  t.after(() => child.kill())
  let observed = false
  for (let attempt = 0; attempt < 30; attempt += 1) {
    if (browserProfileIsInUse(profile)) {
      observed = true
      break
    }
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
  assert.equal(observed, true)
  assert.throws(() => clearStaleBrowserProfileLocks(profile), /profile is already in use/)
  child.kill()
  await once(child, 'exit')
})

function fakeResponse(text) {
  return {
    status: 200,
    headers: {
      get(name) {
        if (String(name).toLowerCase() === 'content-type') return 'application/json'
        return ''
      },
    },
    text,
  }
}

test('new challenge recovery clears stale provider, attempt, and solve timestamps', () => {
  assert.deepEqual(challengeStatusValues({ state: 'queued' }), {
    challenge_state: 'queued',
    challenge_provider: '',
    challenge_attempt: 0,
    challenge_started_at: '',
    challenge_solved_at: '',
  })
  assert.deepEqual(challengeStatusValues({
    state: 'detecting',
    startedAt: '2026-08-06T12:00:00.000Z',
  }), {
    challenge_state: 'detecting',
    challenge_provider: '',
    challenge_attempt: 0,
    challenge_started_at: '2026-08-06T12:00:00.000Z',
    challenge_solved_at: '',
  })
  assert.deepEqual(challengeStatusValues({
    state: 'solving',
    provider: 'aliyun-esa',
    attempt: 1,
  }), {
    challenge_state: 'solving',
    challenge_provider: 'aliyun-esa',
    challenge_attempt: 1,
  })
})

test('pressure recovery preserves a verified context when a job lease is cancelled', () => {
  const controller = new AbortController()
  const reason = Object.assign(new Error('job lease lost'), { kind: 'lease_lost' })
  controller.abort(reason)

  assert.equal(pressureRecoveryFailure({ context: {}, page: {} }, reason, controller.signal), reason)
  assert.equal(reason.restartLane, undefined)

  const interruptedRotation = pressureRecoveryFailure({ context: null, page: null }, reason, controller.signal)
  assert.notEqual(interruptedRotation, reason)
  assert.equal(interruptedRotation.kind, 'lease_lost')
  assert.equal(interruptedRotation.restartLane, true)
  assert.equal(interruptedRotation.cause, reason)
})

test('challenge recovery cancellation rebuilds only when fallback rotation consumed the context', () => {
  const controller = new AbortController()
  const reason = Object.assign(new Error('job lease lost'), { kind: 'lease_lost' })
  controller.abort(reason)

  const intact = laneRecoveryCancellationFailure({ context: {}, page: { isClosed: () => false } }, new Error('challenge failed'), controller.signal)
  assert.equal(intact, reason)
  assert.equal(intact.restartLane, undefined)

  const interrupted = laneRecoveryCancellationFailure({ context: null, page: null }, new Error('challenge failed'), controller.signal)
  assert.notEqual(interrupted, reason)
  assert.equal(interrupted.kind, 'lease_lost')
  assert.equal(interrupted.restartLane, true)
  assert.equal(interrupted.cause, reason)
})

test('job finalization does not discard a required lane restart when the lease signal also aborted', () => {
  const controller = new AbortController()
  const leaseLost = Object.assign(new Error('job lease lost'), { kind: 'lease_lost' })
  controller.abort(leaseLost)
  const interruptedRotation = Object.assign(new Error('job lease lost'), {
    kind: 'lease_lost',
    restartLane: true,
    cause: leaseLost,
  })

  assert.equal(syncJobFinalError(interruptedRotation, controller.signal), interruptedRotation)
  assert.equal(syncJobFinalError(new Error('late request error'), controller.signal), leaseLost)
})

test('browser shop evaluator reports a fetch timeout without throwing', async () => {
  const originalFetch = global.fetch
  let requestSignal
  global.fetch = async (_url, options) => {
    requestSignal = options.signal
    return new Promise(() => {})
  }
  try {
    const result = await evaluateShopRequestInBrowser({
      requestPath: 'https://pay.ldxp.cn/shopApi/Shop/info',
      requestBody: {},
      requestTimeoutMilliseconds: 20,
      visitorID: 'test',
    })
    assert.deepEqual(result, {
      browserTimeout: true,
      timeoutPhase: 'fetch',
      timeoutMilliseconds: 20,
    })
    assert.equal(requestSignal.aborted, true)
  } finally {
    global.fetch = originalFetch
  }
})

test('browser shop evaluator reports a response body timeout without discarding the page', async () => {
  const originalFetch = global.fetch
  let requestSignal
  global.fetch = async (_url, options) => {
    requestSignal = options.signal
    return fakeResponse(() => new Promise(() => {}))
  }
  try {
    const result = await evaluateShopRequestInBrowser({
      requestPath: 'https://pay.ldxp.cn/shopApi/Shop/goodsList',
      requestBody: { goods_type: 'card' },
      requestTimeoutMilliseconds: 20,
      visitorID: 'test',
    })
    assert.equal(result.browserTimeout, true)
    assert.equal(result.timeoutPhase, 'response_body')
    assert.equal(result.timeoutMilliseconds, 20)
    assert.equal(requestSignal.aborted, true)
  } finally {
    global.fetch = originalFetch
  }
})

test('node protocol timeout remains a lane restart and observes a late rejection', async () => {
  let rejectEvaluation
  const page = {
    evaluate: () => new Promise((_, reject) => { rejectEvaluation = reject }),
  }
  const operation = evaluateShopRequest(page, () => null, {}, 15)
  await assert.rejects(operation, (error) => (
    error.restartLane === true
      && error.timeoutPhase === 'protocol'
      && /timed out after 15ms/.test(error.message)
  ))
  rejectEvaluation(new Error('late protocol failure'))
  await new Promise((resolve) => setImmediate(resolve))
})

test('node evaluator cancellation propagates without marking the lane for restart', async () => {
  const controller = new AbortController()
  const page = { evaluate: () => new Promise(() => {}) }
  const operation = evaluateShopRequest(page, () => null, {}, 1000, controller.signal)
  const reason = new Error('lease cancelled')
  controller.abort(reason)
  await assert.rejects(operation, (error) => error === reason && !error.restartLane)
})

test('backend timeout remains active while reading the response body', async () => {
  const originalFetch = global.fetch
  let requestSignal
  global.fetch = async (_url, options) => {
    requestSignal = options.signal
    return {
      ok: true,
      status: 200,
      json: () => new Promise((resolve, reject) => {
        requestSignal.addEventListener('abort', () => reject(requestSignal.reason), { once: true })
      }),
    }
  }
  try {
    await assert.rejects(
      backend('/api/v1/test', {}, 20),
      (error) => error?.name === 'TimeoutError' && /timed out after 20ms/.test(error.message)
    )
    assert.equal(requestSignal.aborted, true)
  } finally {
    global.fetch = originalFetch
  }
})
