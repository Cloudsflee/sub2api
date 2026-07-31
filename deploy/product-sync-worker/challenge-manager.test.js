const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')

const {
  ChallengeError,
  ChallengeManager,
  CancellableMutex,
  challengeBackoffMilliseconds,
  challengeCleared,
  challengeSnapshotDetected,
  collectChallengeSnapshot,
  computeDragDistance,
  defaultChallengeProviders,
  dragSlider,
  generateDragTrajectory,
  isHTTPCustomDenial,
  readStoredSession,
  recoverChallengeAcrossProxyPool,
  retryFailedChallengeOperation,
  selectChallengeProvider,
  sessionFilePath,
  shouldBlockResource,
  writeStoredSession,
} = require('./challenge-manager')

function clearSnapshot(responseError = '') {
  return {
    responseError,
    frames: [{
      title: 'Shop',
      text: 'Products',
      hasCaptchaDOM: false,
      hasAliyunDOM: false,
      hasAliyunScript: false,
      hasGenericSlider: false,
    }],
  }
}

function aliyunSnapshot(overrides = {}) {
  return {
    responseError: '',
    frames: [{
      title: 'Verification',
      text: 'Please slide to verify',
      hasCaptchaDOM: true,
      hasAliyunDOM: true,
      hasAliyunScript: true,
      hasGenericSlider: true,
    }],
    ...overrides,
  }
}

function genericSnapshot() {
  return {
    responseError: '',
    frames: [{
      title: 'Security verification',
      text: 'Drag the control to the right end',
      hasCaptchaDOM: false,
      hasAliyunDOM: false,
      hasAliyunScript: false,
      hasGenericSlider: true,
    }],
  }
}

function sliderGeometry() {
  return {
    trackBox: { x: 20, y: 40, width: 360, height: 40 },
    handleBox: { x: 20, y: 40, width: 40, height: 40 },
  }
}

function temporaryDirectory() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'sub2api-challenge-'))
}

test('provider registry selects Alibaba ESA from headers, scripts, desktop DOM, and child frames', () => {
  const providers = defaultChallengeProviders()
  assert.equal(selectChallengeProvider({
    responseError: 'denied by http_custom',
    frames: [clearSnapshot().frames[0]],
  }, providers).id, 'aliyun-esa')
  assert.equal(selectChallengeProvider(aliyunSnapshot(), providers).id, 'aliyun-esa')
  assert.equal(selectChallengeProvider({
    responseError: '',
    frames: [clearSnapshot().frames[0], aliyunSnapshot().frames[0]],
  }, providers).id, 'aliyun-esa')
})

test('snapshot collection inspects desktop and H5 child frames and response headers', async () => {
  const states = [clearSnapshot().frames[0], aliyunSnapshot().frames[0]]
  const page = {
    frames: () => states.map((state) => ({ evaluate: async () => state })),
  }
  const snapshot = await collectChallengeSnapshot(page, {
    headerValue: async (name) => name === 'x-tengine-error' ? 'denied by http_custom' : '',
  })
  assert.equal(snapshot.frames.length, 2)
  assert.equal(snapshot.frames[1].hasAliyunDOM, true)
  assert.equal(snapshot.responseError, 'denied by http_custom')
  assert.equal(snapshot.isChallenge, true)
})

test('generic slide provider is gated by verification-page classification', () => {
  const providers = defaultChallengeProviders()
  assert.equal(selectChallengeProvider(genericSnapshot(), providers).id, 'generic-slide-to-end')
  assert.equal(selectChallengeProvider({
    responseError: '',
    frames: [{
      ...clearSnapshot().frames[0],
      hasGenericSlider: true,
      text: 'Volume',
    }],
  }, providers), null)
})

test('normal intelligent_cc_acl responses are accepted while http_custom is a challenge', () => {
  assert.equal(challengeSnapshotDetected(clearSnapshot('denied by intelligent_cc_acl')), false)
  assert.equal(challengeSnapshotDetected(clearSnapshot('denied by http_custom')), true)
  assert.equal(isHTTPCustomDenial('Denied by HTTP_CUSTOM'), true)
  const clearedWithLibraryLoaded = clearSnapshot()
  clearedWithLibraryLoaded.frames[0].hasAliyunScript = true
  assert.equal(challengeSnapshotDetected(clearedWithLibraryLoaded), true)
  assert.equal(challengeCleared(clearedWithLibraryLoaded), true)
  assert.equal(challengeCleared(clearSnapshot('denied by http_custom')), false)
})

test('slider distance is derived from the live track and handle right edges', () => {
  assert.equal(computeDragDistance(...[sliderGeometry().trackBox, sliderGeometry().handleBox]), 320)
  assert.equal(computeDragDistance(
    { x: 100, y: 0, width: 420, height: 48 },
    { x: 112, y: 0, width: 48, height: 48 }
  ), 360)
  assert.throws(() => computeDragDistance(
    { x: 0, y: 0, width: 100, height: 30 },
    { x: 0, y: 0, width: 30, height: 30 }
  ), /unsupported/)
})

test('drag trajectories use 36-60 nonlinear steps, bounded duration, jitter, and end correction', () => {
  let state = 17
  const random = () => {
    state = (state * 48271) % 0x7fffffff
    return state / 0x7fffffff
  }
  const trajectory = generateDragTrajectory(320, { random })
  assert.ok(trajectory.steps >= 36 && trajectory.steps <= 60)
  assert.ok(trajectory.durationMilliseconds >= 900 && trajectory.durationMilliseconds <= 1_600)
  assert.equal(trajectory.points.length, trajectory.steps)
  assert.equal(trajectory.points.reduce((sum, point) => sum + point.delayMilliseconds, 0), trajectory.durationMilliseconds)
  assert.deepEqual(trajectory.points.at(-1), {
    x: 320,
    y: 0,
    delayMilliseconds: trajectory.points.at(-1).delayMilliseconds,
  })
  assert.ok(trajectory.points.some((point) => Math.abs(point.y) > 0.1))
  const deltas = trajectory.points.map((point, index) => point.x - (trajectory.points[index - 1]?.x || 0))
  assert.ok(new Set(deltas.map((value) => value.toFixed(3))).size > 5)
  assert.ok(trajectory.points.at(-3).x < trajectory.points.at(-2).x)
  assert.ok(trajectory.points.at(-2).x < trajectory.points.at(-1).x)
})

test('slider drag uses absolute deadlines so mouse protocol time stays inside the trajectory budget', async () => {
  let elapsed = 0
  let pressedAt = 0
  const waits = []
  const events = []
  const page = {
    mouse: {
      move: async (x, y) => {
        events.push(['move', x, y])
        elapsed += 7
      },
      down: async () => {
        pressedAt = elapsed
        events.push(['down'])
      },
      up: async () => events.push(['up']),
    },
  }
  const trajectory = {
    durationMilliseconds: 30,
    points: [
      { x: 10, y: 1, delayMilliseconds: 10 },
      { x: 20, y: -1, delayMilliseconds: 10 },
      { x: 30, y: 0, delayMilliseconds: 10 },
    ],
  }

  await dragSlider(page, sliderGeometry(), trajectory, {
    now: () => elapsed,
    wait: async (milliseconds) => {
      waits.push(milliseconds)
      elapsed += milliseconds
    },
  })

  assert.deepEqual(waits, [10, 3, 3])
  assert.equal(elapsed - pressedAt, 37)
  assert.deepEqual(events.map(([event]) => event), ['move', 'down', 'move', 'move', 'move', 'up'])
})

test('challenge manager succeeds on the second drag, reloads first, and saves storage state', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const cleared = clearSnapshot()
  cleared.frames[0].hasAliyunScript = true
  const snapshots = [aliyunSnapshot(), aliyunSnapshot(), aliyunSnapshot(), cleared]
  const events = []
  let drags = 0
  let reloads = 0
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => sliderGeometry(),
  }
  const context = {
    storageState: async () => ({ cookies: [{ name: 'esa', value: 'ok', domain: '.ldxp.cn', path: '/' }], origins: [] }),
  }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => ({}),
    reload: async () => { reloads += 1; return {} },
    inspect: async () => snapshots.shift(),
    drag: async () => { drags += 1 },
    random: () => 0.5,
  })

  const result = await manager.solve({
    context,
    page: {},
    proxy: { server: 'http://proxy:17891', username: '', password: '' },
    onState: (event) => events.push(event),
  })
  assert.equal(result.state, 'solved')
  assert.equal(result.attempt, 2)
  assert.equal(drags, 2)
  assert.equal(reloads, 1)
  assert.equal(events.at(-1).state, 'solved')
  assert.equal(fs.readdirSync(directory).filter((name) => name.endsWith('.json')).length, 1)
})

test('challenge manager enforces two drag attempts for the lifetime of a context', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  let drags = 0
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => sliderGeometry(),
  }
  const context = { storageState: async () => ({ cookies: [], origins: [] }) }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => ({}),
    reload: async () => ({}),
    inspect: async () => aliyunSnapshot(),
    drag: async () => { drags += 1 },
    random: () => 0.5,
  })

  await assert.rejects(() => manager.solve({ context, page: {} }), (error) => (
    error instanceof ChallengeError && error.challengeState === 'failed'
  ))
  assert.equal(drags, 2)
  await assert.rejects(() => manager.solve({ context, page: {} }), /remained after 2 drag attempts/)
  assert.equal(drags, 2)
})

test('unsupported verification pages do not touch a slider', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  let drags = 0
  const manager = new ChallengeManager({
    enabled: true,
    providers: [],
    sessionDirectory: directory,
    navigate: async () => ({}),
    inspect: async () => ({
      responseError: '',
      frames: [{ ...clearSnapshot().frames[0], title: 'Verification', text: 'Verify that you are a real person' }],
    }),
    drag: async () => { drags += 1 },
  })
  await assert.rejects(() => manager.solve({ context: {}, page: {} }), (error) => (
    error instanceof ChallengeError && error.challengeState === 'unsupported'
  ))
  assert.equal(drags, 0)
})

test('challenge timeout cancels a pending recovery', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const manager = new ChallengeManager({
    enabled: true,
    sessionDirectory: directory,
    timeoutMilliseconds: 20,
    navigate: async () => new Promise(() => {}),
  })
  await assert.rejects(() => manager.solve({ context: {}, page: {} }), (error) => (
    error instanceof ChallengeError && error.challengeState === 'timeout'
  ))
})

test('one shared cancellable lock serializes six lane recoveries', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  let active = 0
  let maximumActive = 0
  const manager = new ChallengeManager({
    enabled: true,
    sessionDirectory: directory,
    navigate: async () => {
      active += 1
      maximumActive = Math.max(maximumActive, active)
      await new Promise((resolve) => setTimeout(resolve, 5))
      active -= 1
      return {}
    },
    inspect: async () => clearSnapshot(),
  })
  const results = await Promise.all(Array.from({ length: 6 }, () => manager.solve({ context: {}, page: {} })))
  assert.equal(maximumActive, 1)
  assert.deepEqual(results.map((result) => result.state), Array(6).fill('clear'))
})

test('six queued lanes each receive a full timeout budget after acquiring the challenge lock', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const mutex = new CancellableMutex()
  const release = await mutex.acquire()
  const manager = new ChallengeManager({
    enabled: true,
    mutex,
    sessionDirectory: directory,
    timeoutMilliseconds: 30,
    navigate: async () => ({}),
    inspect: async () => clearSnapshot(),
  })

  setTimeout(release, 50)
  const results = await Promise.all(Array.from({ length: 6 }, () => manager.solve({ context: {}, page: {} })))
  assert.deepEqual(results.map((result) => result.state), Array(6).fill('clear'))
})

test('a queued lane can cancel without entering the challenge lock', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  let releaseFirst
  let navigations = 0
  const firstGate = new Promise((resolve) => { releaseFirst = resolve })
  const manager = new ChallengeManager({
    enabled: true,
    sessionDirectory: directory,
    navigate: async () => {
      navigations += 1
      if (navigations === 1) await firstGate
      return {}
    },
    inspect: async () => clearSnapshot(),
  })
  const first = manager.solve({ context: {}, page: {} })
  await new Promise((resolve) => setTimeout(resolve, 5))
  const controller = new AbortController()
  const second = manager.solve({ context: {}, page: {}, signal: controller.signal })
  controller.abort(new Error('lane stopped'))
  await assert.rejects(() => second, /lane stopped/)
  releaseFirst()
  await first
  assert.equal(navigations, 1)
})

test('failed API operation is retried in place only after challenge recovery', async () => {
  const events = []
  let attempts = 0
  const result = await retryFailedChallengeOperation(async () => {
    attempts += 1
    events.push(`request-${attempts}`)
    if (attempts === 1) throw new ChallengeError('failed', 'HTML challenge')
    return 'same request completed'
  }, async () => events.push('recover'))
  assert.equal(result, 'same request completed')
  assert.deepEqual(events, ['request-1', 'recover', 'request-2'])

  await assert.rejects(() => retryFailedChallengeOperation(
    async () => { throw new Error('bad catalog payload') },
    async () => {}
  ), /bad catalog payload/)
})

test('challenge recovery switches to a fallback only after the current context fails', async () => {
  const events = []
  const recovered = await recoverChallengeAcrossProxyPool({
    poolSize: 2,
    currentIndex: 0,
    solveCurrent: async () => {
      events.push('primary-two-drags-failed')
      throw new ChallengeError('failed', 'challenge remained')
    },
    switchTo: async (index) => events.push(`fallback-${index}-ready`),
  })
  assert.equal(recovered, 1)
  assert.deepEqual(events, ['primary-two-drags-failed', 'fallback-1-ready'])

  await assert.rejects(() => recoverChallengeAcrossProxyPool({
    poolSize: 2,
    currentIndex: 0,
    solveCurrent: async () => { throw new ChallengeError('failed', 'primary') },
    switchTo: async () => { throw new ChallengeError('failed', 'fallback') },
  }), (error) => error.restartLane === true)
})

test('challenge backoff is independent at 15 minutes, 60 minutes, and 6 hours', () => {
  assert.equal(challengeBackoffMilliseconds(1), 15 * 60_000)
  assert.equal(challengeBackoffMilliseconds(2), 60 * 60_000)
  assert.equal(challengeBackoffMilliseconds(3), 6 * 60 * 60_000)
  assert.equal(challengeBackoffMilliseconds(20), 6 * 60 * 60_000)
  assert.equal(challengeBackoffMilliseconds(1, 'unsupported'), 6 * 60 * 60_000)
})

test('session files are opaque, atomic, private, restorable, and isolated by proxy identity', (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const providers = [{ id: 'aliyun-esa' }, { id: 'generic-slide-to-end' }]
  const proxy = {
    server: 'http://proxy.internal:17891',
    username: 'private-user',
    password: 'private-password',
  }
  const storageState = {
    cookies: [{ name: 'esa', value: 'passed', domain: '.ldxp.cn', path: '/' }],
    origins: [{ origin: 'https://pay.ldxp.cn', localStorage: [{ name: 'token', value: 'ok' }] }],
  }
  const file = writeStoredSession(directory, providers[0].id, 'https://pay.ldxp.cn', proxy, storageState)
  const basename = path.basename(file)
  assert.match(basename, /^[a-f0-9]{64}\.json$/)
  assert.equal(basename.includes(proxy.username), false)
  assert.equal(basename.includes(proxy.password), false)
  assert.deepEqual(readStoredSession(directory, providers, 'https://pay.ldxp.cn', proxy), {
    provider: 'aliyun-esa',
    storageState,
  })
  assert.notEqual(
    sessionFilePath(directory, providers[0].id, 'https://pay.ldxp.cn', proxy),
    sessionFilePath(directory, providers[0].id, 'https://pay.ldxp.cn', { ...proxy, password: 'other' })
  )
  assert.deepEqual(fs.readdirSync(directory), [basename])
  if (process.platform !== 'win32') {
    assert.equal(fs.statSync(directory).mode & 0o777, 0o700)
    assert.equal(fs.statSync(file).mode & 0o777, 0o600)
  }
})

test('corrupt session state is ignored and removed for automatic rebuilding', (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const providers = [{ id: 'aliyun-esa' }]
  const file = sessionFilePath(directory, providers[0].id, 'https://pay.ldxp.cn', null)
  fs.writeFileSync(file, '{broken')
  assert.equal(readStoredSession(directory, providers, 'https://pay.ldxp.cn', null), null)
  assert.equal(fs.existsSync(file), false)
})

test('resource blocking is disabled only while challenge assets are needed', () => {
  assert.equal(shouldBlockResource('image', false), true)
  assert.equal(shouldBlockResource('font', false), true)
  assert.equal(shouldBlockResource('media', true), false)
  assert.equal(shouldBlockResource('script', false), false)
})

test('cancellable mutex hands the lock to the next live waiter', async () => {
  const mutex = new CancellableMutex()
  const release = await mutex.acquire()
  const cancelled = new AbortController()
  const cancelledWaiter = mutex.acquire(cancelled.signal)
  const liveWaiter = mutex.acquire()
  cancelled.abort(new Error('cancelled'))
  await assert.rejects(() => cancelledWaiter, /cancelled/)
  release()
  const releaseLive = await liveWaiter
  releaseLive()
  assert.equal(mutex.locked, false)
})
