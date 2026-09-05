const test = require('node:test')
const assert = require('node:assert/strict')
const {
  AdaptiveRateDeadlineError,
  AdaptiveRateLimiter,
  buildWAFResponseFingerprint,
  parseRetryAfterMilliseconds,
  proxyEgressID,
} = require('./adaptive-rate')

function fakeClock(start = 0) {
  let current = start
  return {
    now: () => current,
    set: (value) => { current = value },
    sleep: async (milliseconds) => { current += milliseconds },
  }
}

test('adaptive limiter starts at 1.5 requests per second with no burst', async () => {
  const clock = fakeClock()
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })

  assert.equal(await limiter.take(), 0)
  const second = await limiter.take()
  const third = await limiter.take()

  assert.ok(second >= 667 && second <= 668)
  assert.ok(third >= 1334 && third <= 1336)
  assert.equal(limiter.snapshot().adaptive_rate_per_second, 1.5)
})

test('403 and 429 halve the adaptive rate down to the 0.5 floor', () => {
  const clock = fakeClock(10_000)
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })

  const denied = limiter.recordRatePressure('access_denied', { egressID: 'egress-a' })
  assert.equal(denied.adaptive_rate_per_second, 0.75)
  assert.equal(denied.egress_circuits[0].egress_id, 'egress-a')
  assert.equal(Date.parse(denied.egress_circuits[0].circuit_open_until), 610_000)

  limiter.recordRatePressure('rate_limit', { retryAfterMilliseconds: 15_000 })
  assert.equal(limiter.snapshot().adaptive_rate_per_second, 0.5)
  limiter.recordRatePressure('rate_limit')
  assert.equal(limiter.snapshot().adaptive_rate_per_second, 0.5)
})

test('two distinct 403 exits within 60 seconds trigger a 120 second global silence', async () => {
  const clock = fakeClock(1_000)
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })

  limiter.recordRatePressure('access_denied', { egressID: 'egress-a' })
  clock.set(60_999)
  const pressure = limiter.recordRatePressure('access_denied', { egressID: 'egress-b' })

  assert.equal(pressure.adaptive_rate_per_second, 0.5)
  assert.equal(pressure.global_pressure_state, 'silent')
  assert.equal(Date.parse(pressure.global_silence_until), 180_999)
  assert.equal(await limiter.take(), 180_999)
})

test('403 events outside the cluster window do not globally silence requests', () => {
  const clock = fakeClock(1_000)
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })
  limiter.recordRatePressure('access_denied', { egressID: 'egress-a' })
  clock.set(61_001)
  const pressure = limiter.recordRatePressure('access_denied', { egressID: 'egress-b' })
  assert.equal(pressure.global_pressure_state, 'pressured')
  assert.equal(pressure.global_silence_until, '')
})

test('100 successes after five pressure-free minutes restore 0.1 requests per second', () => {
  const clock = fakeClock()
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })
  limiter.recordRatePressure('rate_limit')
  clock.set(5 * 60_000)
  for (let index = 0; index < 99; index += 1) limiter.recordSuccess('egress-a')
  assert.equal(limiter.snapshot().adaptive_rate_per_second, 0.75)
  const recovered = limiter.recordSuccess('egress-a')
  assert.equal(recovered.adaptive_rate_per_second, 0.85)
  assert.equal(recovered.adaptive_success_streak, 0)
})

test('transport failure counters are isolated per exit and rotate on two consecutive failures', () => {
  const limiter = new AdaptiveRateLimiter()
  limiter.recordSuccess('egress-a')
  assert.deepEqual(limiter.recordTransportFailure('egress-a'), { failureCount: 1, rotate: false })
  assert.equal(limiter.snapshot().adaptive_success_streak, 0)
  assert.deepEqual(limiter.recordTransportFailure('egress-b'), { failureCount: 1, rotate: false })
  assert.deepEqual(limiter.recordTransportFailure('egress-a'), { failureCount: 2, rotate: true })
  limiter.recordSuccess('egress-b')
  assert.deepEqual(limiter.recordTransportFailure('egress-b'), { failureCount: 1, rotate: false })
})

test('fallback selection skips an exit with an open circuit', () => {
  const clock = fakeClock()
  const primary = { server: 'http://primary.test:1000' }
  const fallback = { server: 'http://fallback.test:2000' }
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })
  limiter.recordRatePressure('access_denied', { egressID: proxyEgressID(primary) })
  assert.equal(limiter.nextAvailableProxyIndex([primary, fallback], 0), 1)
  limiter.recordRatePressure('access_denied', { egressID: proxyEgressID(fallback) })
  assert.equal(limiter.nextAvailableProxyIndex([primary, fallback], 0), -1)
})

test('Retry-After accepts seconds and HTTP dates', () => {
  const now = Date.parse('2026-09-05T00:00:00Z')
  assert.equal(parseRetryAfterMilliseconds('12.5', now), 12_500)
  assert.equal(parseRetryAfterMilliseconds('Sat, 05 Sep 2026 00:00:30 GMT', now), 30_000)
  assert.equal(parseRetryAfterMilliseconds('invalid', now), 0)
})

test('adaptive waits remain cancellable and reject waits beyond a deadline', async () => {
  const clock = fakeClock()
  const limiter = new AdaptiveRateLimiter({ now: clock.now, sleep: clock.sleep })
  limiter.recordRatePressure('access_denied', { egressID: 'egress-a' })
  limiter.recordRatePressure('access_denied', { egressID: 'egress-b' })

  await assert.rejects(limiter.take(undefined, 60_000), AdaptiveRateDeadlineError)
  const controller = new AbortController()
  controller.abort(new Error('lease lost'))
  await assert.rejects(limiter.take(controller.signal), /lease lost/)
})

test('WAF fingerprint is bounded and redacts credentials, cookies, tokens, and signatures', () => {
  const fingerprint = buildWAFResponseFingerprint({
    status: 403,
    server: 'Tengine token=server-secret',
    responseError: 'denied signature=abc123',
    bodyLength: 999,
    bodySHA256: 'a'.repeat(64),
    text: '<html><title>Denied token=secret Cookie: session-id</title></html>',
  }, {
    path: '/shopApi/Shop/getGoodsPrice?token=shop-secret&signature=signed',
    egressID: 'egress-123',
    now: 0,
  })

  assert.deepEqual(fingerprint, {
    observed_at: '1970-01-01T00:00:00.000Z',
    status: 403,
    api_path: '/shopApi/Shop/getGoodsPrice',
    server: 'Tengine token=***',
    x_tengine_error: 'denied signature=***',
    body_length: 999,
    body_sha256: 'a'.repeat(64),
    title: 'Denied token=*** Cookie=***',
    egress_id: 'egress-123',
  })
})
