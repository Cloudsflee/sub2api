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
  challengeOperationCompletion,
  challengeStatusValues,
  backend,
  encodeShopRequestBody,
  evaluateShopRequest,
  evaluateShopRequestWithContext,
  evaluateShopRequestInBrowser,
  laneRecoveryCancellationFailure,
  pressureRecoveryFailure,
  resetChallengeAttemptState,
  shopSessionOriginState,
  shopRequestUsesContextTransport,
  retryAccessDeniedAfterHomeReview,
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

  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 5000)', '--', '-profile', profile], { stdio: 'ignore' })
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

test('challenge callback completion accepts only successful object payloads', () => {
  const payload = { code: 1, data: { verified: true } }
  assert.deepEqual(challengeOperationCompletion({
    operationResponse: { status: 200, payload },
  }), { completed: true, value: payload })
  assert.equal(challengeOperationCompletion({ operationResponse: { status: 403, payload } }), null)
  assert.equal(challengeOperationCompletion({ operationResponse: { status: 200, payload: null } }), null)
  assert.equal(challengeOperationCompletion({
    operationResponse: { status: 200, payload: { code: 0, msg: '店铺链接不存在', data: null } },
  }), null)
  assert.equal(challengeOperationCompletion({
    operationResponse: { status: 200, payload: { code: 0, msg: '店铺链接不存在', data: {} } },
  }), null)
  assert.equal(challengeOperationCompletion({
    operationResponse: { status: 200, payload: { code: 1, data: null } },
  }), null)
  assert.equal(challengeOperationCompletion(null), null)
})

test('a lane fallback receives a fresh per-operation challenge budget', () => {
  const state = { attempt: 4, stage: 2, stageAttempt: 2 }
  assert.equal(resetChallengeAttemptState(state), state)
  assert.deepEqual(state, { attempt: 0, stage: 1, stageAttempt: 0 })
})

test('shop session accepts the exact ESA callback origin only as a challenge landing page', () => {
  assert.deepEqual(shopSessionOriginState({
    frames: [
      { url: 'https://wzyp.cn/shopApi/Shop/info?u_atoken=redacted' },
      { url: 'https://captcha.example/frame' },
    ],
  }), {
    reachedShopOrigin: false,
    reachedChallengeCallbackOrigin: true,
  })
  assert.deepEqual(shopSessionOriginState({
    frames: [{ url: 'https://pay.ldxp.cn/' }],
  }), {
    reachedShopOrigin: true,
    reachedChallengeCallbackOrigin: false,
  })
  assert.deepEqual(shopSessionOriginState({
    frames: [{ url: 'https://example.com/' }],
  }), {
    reachedShopOrigin: false,
    reachedChallengeCallbackOrigin: false,
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

test('access denial home review retries only the failed API after a solved challenge', async () => {
  let attempts = 0
  let reviews = 0
  const state = { reviewed: false }
  const result = await retryAccessDeniedAfterHomeReview(async () => {
    attempts += 1
    if (attempts === 1) throw Object.assign(new Error('HTTP 403'), { kind: 'access_denied' })
    return 'api result'
  }, async () => {
    reviews += 1
    return { challengeDetected: true, challengeSolved: true }
  }, { state })

  assert.equal(result, 'api result')
  assert.equal(attempts, 2)
  assert.equal(reviews, 1)
  assert.equal(state.reviewed, true)
})

test('access denial with a clear home page flows into pressure recovery without an API retry', async () => {
  let attempts = 0
  const denial = Object.assign(new Error('HTTP 403'), { kind: 'access_denied' })
  await assert.rejects(() => retryAccessDeniedAfterHomeReview(async () => {
    attempts += 1
    throw denial
  }, async () => ({ challengeDetected: false, challengeSolved: false })), (error) => error === denial)
  assert.equal(attempts, 1)
})

test('an API operation performs at most one immediate access denial home review', async () => {
  let reviews = 0
  const state = { reviewed: false }
  const denial = Object.assign(new Error('HTTP 403'), { kind: 'access_denied' })
  const task = async () => { throw denial }

  await assert.rejects(() => retryAccessDeniedAfterHomeReview(task, async () => {
    reviews += 1
    return { challengeDetected: false }
  }, { state }), (error) => error === denial)
  await assert.rejects(() => retryAccessDeniedAfterHomeReview(task, async () => {
    reviews += 1
    return { challengeDetected: false }
  }, { state }), (error) => error === denial)
  assert.equal(reviews, 1)
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

test('browser shop evaluator form-encodes fields for ESA callback replay', async () => {
  const originalFetch = global.fetch
  let captured
  global.fetch = async (url, options) => {
    captured = { url, options }
    return fakeResponse(async () => '{"code":1,"data":{}}')
  }
  try {
    const result = await evaluateShopRequestInBrowser({
      requestPath: 'https://pay.ldxp.cn/shopApi/Shop/info',
      requestBody: { token: 'shop-token', category_key: null, quantity: 2 },
      requestTimeoutMilliseconds: 1_000,
      visitorID: 'visitor',
    })

    assert.equal(result.status, 200)
    assert.equal(captured.options.headers['Content-Type'], 'application/x-www-form-urlencoded;charset=UTF-8')
    assert.equal(captured.options.headers.Visitorid, 'visitor')
    assert.equal(captured.options.body, 'token=shop-token&category_key=&quantity=2')
  } finally {
    global.fetch = originalFetch
  }
})

test('shop request form encoding preserves blank, scalar, array, and object fields', () => {
  assert.equal(encodeShopRequestBody({
    token: 'shop-token',
    category_key: null,
    quantity: 2,
    tags: ['a', 'b'],
    filter: { active: true },
  }), 'token=shop-token&category_key=&quantity=2&tags=a&tags=b&filter=%7B%22active%22%3Atrue%7D')
})

test('shop requests use the browser-context transport only after an ESA callback changes page origin', () => {
  const context = { request: { fetch: async () => {} } }
  assert.equal(shopRequestUsesContextTransport({
    context,
    page: { url: () => 'https://pay.ldxp.cn/' },
  }), false)
  assert.equal(shopRequestUsesContextTransport({
    context,
    page: { url: () => 'https://wzyp.cn/shopApi/Shop/info?u_atoken=redacted' },
  }), true)
  assert.equal(shopRequestUsesContextTransport({
    context: {},
    page: { url: () => 'https://wzyp.cn/' },
  }), false)
  assert.equal(shopRequestUsesContextTransport({
    context,
    page: { url: () => 'https://example.com/' },
  }), false)
})

test('browser-context shop transport preserves the ESA form body and response metadata without CORS', async () => {
  let captured
  let disposed = false
  const context = {
    request: {
      fetch: async (url, options) => {
        captured = { url, options }
        return {
          status: () => 200,
          url: () => 'https://pay.ldxp.cn/shopApi/Shop/goodsList',
          headers: async () => ({ 'content-type': 'application/json', 'retry-after': '2' }),
          text: async () => '{"code":1,"data":{"total":1,"list":[]}}',
          dispose: async () => { disposed = true },
        }
      },
    },
  }

  const result = await evaluateShopRequestWithContext(context, {
    requestPath: 'https://pay.ldxp.cn/shopApi/Shop/goodsList',
    requestFormBody: 'token=shop-token&goods_type=card',
    requestTimeoutMilliseconds: 1_000,
    visitorID: 'visitor-1',
  }, 1_500)

  assert.equal(captured.url, 'https://pay.ldxp.cn/shopApi/Shop/goodsList')
  assert.equal(captured.options.method, 'POST')
  assert.equal(captured.options.data, 'token=shop-token&goods_type=card')
  assert.equal(captured.options.headers.Visitorid, 'visitor-1')
  assert.equal(captured.options.timeout, 1_000)
  assert.equal(captured.options.maxRedirects, 0)
  assert.deepEqual(result.payload, { code: 1, data: { total: 1, list: [] } })
  assert.equal(result.retryAfter, '2')
  assert.equal(result.redirectCount, 0)
  assert.equal(disposed, true)
})

test('browser-context shop transport preserves POST across the trusted callback redirect', async () => {
  const calls = []
  let disposedRedirect = false
  let disposedFinal = false
  const context = {
    request: {
      fetch: async (url, options) => {
        calls.push({ url, options })
        if (calls.length === 1) {
          return {
            status: () => 302,
            url: () => url,
            headers: async () => ({
              location: 'https://wzyp.cn/shopApi/Shop/goodsList?u_atoken=opaque&u_asig=opaque',
            }),
            dispose: async () => { disposedRedirect = true },
          }
        }
        return {
          status: () => 200,
          url: () => url,
          headers: async () => ({ 'content-type': 'application/json' }),
          text: async () => '{"code":1,"data":{"total":2,"list":[]}}',
          dispose: async () => { disposedFinal = true },
        }
      },
    },
  }

  const result = await evaluateShopRequestWithContext(context, {
    requestPath: 'https://pay.ldxp.cn/shopApi/Shop/goodsList',
    requestFormBody: 'token=shop-token&goods_type=card',
    requestTimeoutMilliseconds: 1_000,
    visitorID: 'visitor-1',
  }, 1_500)

  assert.deepEqual(calls.map((call) => call.url), [
    'https://pay.ldxp.cn/shopApi/Shop/goodsList',
    'https://wzyp.cn/shopApi/Shop/goodsList?u_atoken=opaque&u_asig=opaque',
  ])
  assert.deepEqual(calls.map((call) => call.options.method), ['POST', 'POST'])
  assert.deepEqual(calls.map((call) => call.options.data), [
    'token=shop-token&goods_type=card',
    'token=shop-token&goods_type=card',
  ])
  assert.equal(result.redirectCount, 1)
  assert.equal(result.payload.code, 1)
  assert.equal(disposedRedirect, true)
  assert.equal(disposedFinal, true)
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

test('browser shop evaluator preserves a bounded HTML challenge and its final response URL', async () => {
  const originalFetch = global.fetch
  const requestURL = 'https://pay.ldxp.cn/shopApi/Shop/info'
  const responseURL = `${requestURL}?challenge=1`
  const body = `<div id="captcha-element">Please slide to verify</div>${'x'.repeat(520_000)}`
  global.fetch = async () => ({
    status: 403,
    url: responseURL,
    headers: {
      get(name) {
        const header = String(name).toLowerCase()
        if (header === 'content-type') return 'text/html; charset=utf-8'
        if (header === 'x-tengine-error') return 'denied by http_custom'
        return ''
      },
    },
    text: async () => body,
  })
  try {
    const result = await evaluateShopRequestInBrowser({
      requestPath: requestURL,
      requestBody: {},
      requestTimeoutMilliseconds: 1_000,
      visitorID: 'test',
    })
    assert.equal(result.status, 403)
    assert.equal(result.responseURL, responseURL)
    assert.equal(result.text, body.slice(0, 512_000))
    assert.equal(result.responseError, 'denied by http_custom')
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
