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
  challengeContentCleared,
  challengeCleared,
  challengeSnapshotDetected,
  collectChallengeSnapshot,
  computeDragDistance,
  defaultChallengeProviders,
  dragSlider,
  dragSliderNative,
  generateDragTrajectory,
  isHTTPCustomDenial,
  readStoredSession,
  recoverChallengeAcrossProxyPool,
  resizeNativeBrowserWindow,
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

test('post-drag normal document is accepted when ESA leaves a stale http_custom header', () => {
  const snapshot = clearSnapshot('denied by http_custom')
  snapshot.frames[0].url = 'https://pay.ldxp.cn/'
  assert.equal(challengeCleared(snapshot), false)
  assert.equal(challengeContentCleared(snapshot), true)
  assert.equal(challengeContentCleared({ ...snapshot, frames: [{ ...snapshot.frames[0], text: '请按住滑块，拖动到最右边' }] }), false)
  assert.equal(challengeContentCleared({ ...snapshot, frames: [{ ...snapshot.frames[0], url: 'about:blank' }] }), false)
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

test('drag trajectories preserve the measured hover, late acceleration, overshoot, and release hold', () => {
  let state = 17
  const random = () => {
    state = (state * 48271) % 0x7fffffff
    return state / 0x7fffffff
  }
  const trajectory = generateDragTrajectory(320, { random })
  assert.ok(trajectory.steps >= 50 && trajectory.steps <= 60)
  assert.ok(trajectory.travelDurationMilliseconds >= 1_000 && trajectory.travelDurationMilliseconds <= 1_400)
  assert.ok(trajectory.settleDurationMilliseconds >= 450 && trajectory.settleDurationMilliseconds <= 750)
  assert.ok(trajectory.approachStartX >= 320 * 0.58 && trajectory.approachStartX <= 320 * 0.75)
  assert.ok(trajectory.approachStartY >= -8 && trajectory.approachStartY <= 4)
  assert.ok(trajectory.approachStartHoldMilliseconds >= 900 && trajectory.approachStartHoldMilliseconds <= 1_600)
  assert.ok(trajectory.approachDurationMilliseconds >= 1_100 && trajectory.approachDurationMilliseconds <= 1_700)
  assert.ok(trajectory.approachPoints.length >= 30 && trajectory.approachPoints.length <= 42)
  assert.equal(
    trajectory.approachPoints.reduce((sum, point) => sum + point.delayMilliseconds, 0),
    trajectory.approachDurationMilliseconds
  )
  assert.deepEqual(trajectory.approachPoints.at(-1), {
    x: 0,
    y: 0,
    delayMilliseconds: trajectory.approachPoints.at(-1).delayMilliseconds,
  })
  assert.ok(trajectory.hoverMilliseconds >= 800 && trajectory.hoverMilliseconds <= 1_400)
  assert.ok(trajectory.holdMilliseconds >= 550 && trajectory.holdMilliseconds <= 850)
  assert.ok(trajectory.overshootPixels >= 24 && trajectory.overshootPixels <= 42)
  assert.ok(trajectory.startOffsetX >= -3 && trajectory.startOffsetX <= 5)
  assert.ok(trajectory.startOffsetY >= 2 && trajectory.startOffsetY <= 7)
  assert.equal(trajectory.points.length, trajectory.steps)
  assert.equal(trajectory.points.reduce((sum, point) => sum + point.delayMilliseconds, 0), trajectory.durationMilliseconds)
  const trackEndIndex = trajectory.points.findIndex((point) => point.x >= 320)
  assert.ok(trackEndIndex > 35)
  assert.equal(trajectory.points[trackEndIndex].x, 320)
  assert.equal(
    trajectory.points.slice(0, trackEndIndex + 1).reduce((sum, point) => sum + point.delayMilliseconds, 0),
    trajectory.travelDurationMilliseconds
  )
  assert.equal(trajectory.points.at(-1).x, 320 + trajectory.overshootPixels)
  assert.ok(Math.abs(trajectory.points.at(-1).y) <= 3)
  assert.ok(trajectory.points.some((point) => Math.abs(point.y) > 0.5))
  const deltas = trajectory.points.map((point, index) => point.x - (trajectory.points[index - 1]?.x || 0))
  assert.ok(new Set(deltas.map((value) => value.toFixed(3))).size > 5)
  assert.ok(deltas.every((delta) => delta > 0))
  assert.ok(trajectory.points.at(-2).x < trajectory.points.at(-1).x)
  const verticalDeltas = trajectory.points.slice(1).map((point, index) => Math.abs(point.y - trajectory.points[index].y))
  assert.ok(Math.max(...verticalDeltas) < 3)
})

test('slider drag applies a human approach, start offset, hover dwell, and release hold', async () => {
  let elapsed = 0
  const waits = []
  const events = []
  const page = {
    mouse: {
      move: async (x, y) => events.push(['move', x, y]),
      down: async () => events.push(['down']),
      up: async () => events.push(['up']),
    },
  }
  await dragSlider(page, sliderGeometry(), {
    startOffsetX: 4,
    startOffsetY: 6,
    approachStartX: 220,
    approachStartY: -6,
    approachStartHoldMilliseconds: 1_410,
    approachPoints: [{ x: 0, y: 0, delayMilliseconds: 1_437 }],
    hoverMilliseconds: 1_868,
    holdMilliseconds: 751,
    points: [{ x: 358, y: -2, delayMilliseconds: 1_120 }],
  }, {
    now: () => elapsed,
    wait: async (milliseconds) => {
      waits.push(milliseconds)
      elapsed += milliseconds
    },
  })
  assert.deepEqual(events, [
    ['move', 264, 60],
    ['move', 44, 66],
    ['down'],
    ['move', 402, 64],
    ['up'],
  ])
  assert.deepEqual(waits, [1_410, 1_437, 1_868, 1_120, 751])
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

test('native slider drag focuses X11, skips duplicate pixels, and always releases the button', async () => {
  const commands = []
  const page = {
    bringToFront: async () => commands.push(['front']),
    title: async () => '滑动验证页面',
  }
  await dragSliderNative(page, sliderGeometry(), {
    approachStartX: 80,
    approachStartY: -5,
    approachStartHoldMilliseconds: 1,
    approachPoints: [
      { x: 40, y: -2, delayMilliseconds: 1 },
      { x: 0, y: 0, delayMilliseconds: 1 },
    ],
    hoverMilliseconds: 1,
    points: [
      { x: 0.2, y: 0, delayMilliseconds: 1 },
      { x: 0.4, y: 0, delayMilliseconds: 1 },
      { x: 10.2, y: 1, delayMilliseconds: 1 },
      { x: 320, y: 0, delayMilliseconds: 1 },
    ],
  }, {
    screenGeometry: { left: 100, top: 200, scale: 1 },
    nativeWindowPID: 42,
    executeXdotool: async (args) => {
      commands.push(args)
      if (args[0] === 'search') return { stdout: '101\n102\n' }
      if (args[0] === 'getwindowname') {
        return { stdout: args[1] === '102' ? '滑动验证页面 — Camoufox\n' : 'Camoufox\n' }
      }
      return { stdout: '' }
    },
    wait: async () => {},
  })
  assert.deepEqual(commands, [
    ['front'],
    ['search', '--onlyvisible', '--pid', 42],
    ['getwindowname', '101'],
    ['getwindowname', '102'],
    ['windowraise', '102'],
    ['windowfocus', '--sync', '102'],
    ['mousemove', '--sync', 220, 255],
    ['mousemove', 180, 258],
    ['mousemove', 140, 260],
    ['mousedown', '1'],
    ['mousemove', 150, 261],
    ['mousemove', 460, 260],
    ['mouseup', '1'],
  ])
})

test('native browser resize finds its PID window without stealing focus', async () => {
  const commands = []
  const windowID = await resizeNativeBrowserWindow(42, 1024, 824, {
    executeXdotool: async (args) => {
      commands.push(args)
      if (args[0] === 'search') return { stdout: '101\n102\n' }
      return { stdout: '' }
    },
  })

  assert.equal(windowID, 101)
  assert.deepEqual(commands, [
    ['search', '--onlyvisible', '--pid', 42],
    ['windowsize', '--sync', '101', 1024, 824],
  ])
})

test('native slider drag with no target PID falls back before sending X11 input', async () => {
  const x11Commands = []
  const mouseEvents = []
  const page = {
    bringToFront: async () => {},
    mouse: {
      move: async (x, y) => mouseEvents.push(['move', x, y]),
      down: async () => mouseEvents.push(['down']),
      up: async () => mouseEvents.push(['up']),
    },
  }

  await dragSlider(page, sliderGeometry(), {
    points: [{ x: 320, y: 0, delayMilliseconds: 0 }],
  }, {
    nativeDrag: true,
    executeXdotool: async (args) => x11Commands.push(args),
    wait: async () => {},
  })

  assert.deepEqual(x11Commands, [])
  assert.deepEqual(mouseEvents.map(([event]) => event), ['move', 'down', 'move', 'up'])
})

test('native slider drag calibrates the headed Chrome viewport origin from a trusted move', async () => {
  const commands = []
  let evaluations = 0
  const page = {
    bringToFront: async () => {},
    evaluate: async (_script, argument) => {
      evaluations += 1
      if (evaluations === 1) {
        return {
          screenX: 10,
          screenY: 10,
          outerWidth: 1032,
          outerHeight: 899,
          innerWidth: 1024,
          innerHeight: 768,
          devicePixelRatio: 1,
        }
      }
      if (evaluations === 3) return { clientX: 8, clientY: 50, screenX: 108, screenY: 210 }
      return undefined
    },
  }
  await dragSliderNative(page, sliderGeometry(), {
    points: [{ x: 320, y: 0, delayMilliseconds: 1 }],
  }, {
    nativeWindowPID: 77,
    executeXdotool: async (args) => {
      commands.push(args)
      if (args[0] === 'search') return { stdout: '701\n' }
      return { stdout: '' }
    },
    wait: async () => {},
  })
  assert.deepEqual(commands, [
    ['search', '--onlyvisible', '--pid', 77],
    ['windowraise', '701'],
    ['windowfocus', '--sync', '701'],
    ['mousemove', '--sync', 22, 897],
    ['mousemove', '--sync', 140, 220],
    ['mousedown', '1'],
    ['mousemove', 460, 220],
    ['mouseup', '1'],
  ])
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

test('challenge manager waits for the ESA success navigation before issuing a home request', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const snapshots = [aliyunSnapshot(), clearSnapshot()]
  let navigateCalls = 0
  let dragCalls = 0
  let navigationArmed = false
  let navigationTimeout = 0
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => sliderGeometry(),
  }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => {
      navigateCalls += 1
      return {}
    },
    inspect: async () => snapshots.shift(),
    drag: async () => {
      assert.equal(navigationArmed, true)
      dragCalls += 1
    },
    random: () => 0.5,
  })
  const page = {
    // The real ESA callback navigates to the protected URL with u_atoken and
    // u_asig.  Resolving this promise simulates that callback completing.
    waitForNavigation: async (options) => {
      navigationArmed = true
      navigationTimeout = options.timeout
      return {}
    },
  }
  const result = await manager.solve({
    context: { storageState: async () => ({ cookies: [], origins: [] }) },
    page,
  })
  assert.equal(result.state, 'solved')
  assert.equal(dragCalls, 1)
  assert.equal(navigateCalls, 1)
  assert.equal(navigationTimeout, 15_000)
})

test('challenge manager reinspects the ESA page when its callback aborts the fallback navigation', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const snapshots = [aliyunSnapshot(), clearSnapshot()]
  let gotoCalls = 0
  let loadStateCalls = 0
  const manager = new ChallengeManager({
    enabled: true,
    providers: [{
      id: 'aliyun-esa',
      detect: () => true,
      locate: async () => sliderGeometry(),
    }],
    sessionDirectory: directory,
    inspect: async () => snapshots.shift(),
    drag: async () => {},
    random: () => 0.5,
  })
  const page = {
    goto: async () => {
      gotoCalls += 1
      if (gotoCalls === 1) return {}
      throw new Error('page.goto: NS_BINDING_ABORTED')
    },
    waitForNavigation: async () => {
      throw new Error('navigation timeout')
    },
    waitForLoadState: async () => {
      loadStateCalls += 1
    },
  }

  const result = await manager.solve({
    context: { storageState: async () => ({ cookies: [], origins: [] }) },
    page,
  })

  assert.equal(result.state, 'solved')
  assert.equal(gotoCalls, 2)
  assert.equal(loadStateCalls, 1)
})

test('challenge manager observes a closed-page navigation waiter and falls back without an unhandled rejection', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const snapshots = [aliyunSnapshot(), clearSnapshot()]
  let navigateCalls = 0
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => sliderGeometry(),
  }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => {
      navigateCalls += 1
      return {}
    },
    inspect: async () => snapshots.shift(),
    drag: async () => {},
    random: () => 0.5,
  })
  const page = {
    waitForNavigation: async () => {
      throw new Error('page closed')
    },
  }
  const result = await manager.solve({
    context: { storageState: async () => ({ cookies: [], origins: [] }) },
    page,
  })
  assert.equal(result.state, 'solved')
  assert.equal(navigateCalls, 2)
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
