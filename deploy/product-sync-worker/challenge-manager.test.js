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
  stageChallengeResponse,
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

test('snapshot collection ignores hidden captcha and slider placeholders', async (t) => {
  const previousDocument = global.document
  const previousLocation = global.location
  const previousGetComputedStyle = global.getComputedStyle
  const hiddenElement = {
    closest: () => null,
    getBoundingClientRect: () => ({ width: 1, height: 1 }),
  }
  global.document = {
    body: { innerText: 'Products' },
    scripts: [],
    title: 'Shop',
    querySelector: () => hiddenElement,
    querySelectorAll: () => [hiddenElement],
  }
  global.location = { href: 'https://pay.ldxp.cn/' }
  global.getComputedStyle = () => ({ display: 'none', visibility: 'visible', opacity: '1' })
  t.after(() => {
    global.document = previousDocument
    global.location = previousLocation
    global.getComputedStyle = previousGetComputedStyle
  })

  const snapshot = await collectChallengeSnapshot({
    frames: () => [{ evaluate: async (fn) => fn() }],
  }, null)

  assert.equal(snapshot.frames[0].hasCaptchaDOM, false)
  assert.equal(snapshot.frames[0].hasAliyunDOM, false)
  assert.equal(snapshot.frames[0].hasGenericSlider, false)
  assert.equal(snapshot.isChallenge, false)
})

test('generic slide provider is gated by verification-page classification', () => {
  const providers = defaultChallengeProviders()
  assert.equal(selectChallengeProvider(genericSnapshot(), providers).id, 'generic-slide-to-end')
  assert.equal(selectChallengeProvider({
    responseError: '',
    frames: [
      { ...clearSnapshot().frames[0], title: 'Security verification', text: 'Please complete verification' },
      { ...clearSnapshot().frames[0], title: '', text: '', hasGenericSlider: true },
    ],
  }, providers).id, 'generic-slide-to-end')
  assert.equal(selectChallengeProvider({
    responseError: 'denied by http_custom',
    frames: [{
      ...clearSnapshot().frames[0],
      hasGenericSlider: true,
      title: '',
      text: '',
    }],
  }, providers).id, 'generic-slide-to-end')
  assert.equal(selectChallengeProvider({
    responseError: '',
    frames: [{
      ...clearSnapshot().frames[0],
      hasGenericSlider: true,
      text: 'Volume',
    }],
  }, providers), null)
})

test('stale ESA header plus a normal-page slider does not select a drag provider', () => {
  const providers = defaultChallengeProviders()
  assert.equal(selectChallengeProvider({
    responseError: 'denied by http_custom',
    responseContext: 'post-drag-submission',
    frames: [{
      ...clearSnapshot().frames[0],
      url: 'https://pay.ldxp.cn/',
      hasGenericSlider: true,
      title: 'Shop',
      text: 'Products and volume controls',
    }],
  }, providers), null)
  assert.equal(challengeContentCleared({
    responseError: 'denied by http_custom',
    responseContext: 'post-drag-submission',
    frames: [{
      ...clearSnapshot().frames[0],
      url: 'https://pay.ldxp.cn/',
      hasGenericSlider: true,
    }],
  }), true)
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
  assert.equal(challengeCleared({
    responseError: '',
    frames: [{ ...clearSnapshot().frames[0], text: 'denied by http_custom' }],
  }), false)
})

test('a visible generic slider keeps a classified verification page uncleared', () => {
  const postDrag = {
    responseError: '',
    frames: [{
      ...clearSnapshot().frames[0],
      hasGenericSlider: true,
    }],
  }
  assert.equal(challengeSnapshotDetected(postDrag), false)
  assert.equal(challengeCleared(postDrag), false)
  assert.equal(challengeContentCleared({
    ...postDrag,
    frames: [{ ...postDrag.frames[0], url: 'https://pay.ldxp.cn/' }],
  }), false)
})

test('post-drag normal document is accepted when ESA leaves a stale http_custom header', () => {
  const snapshot = clearSnapshot('denied by http_custom')
  snapshot.frames[0].url = 'https://pay.ldxp.cn/'
  assert.equal(challengeCleared(snapshot), false)
  assert.equal(challengeContentCleared(snapshot), true)
  assert.equal(challengeContentCleared({ ...snapshot, frames: [{ ...snapshot.frames[0], text: '请按住滑块，拖动到最右边' }] }), false)
  assert.equal(challengeContentCleared({ ...snapshot, frames: [{ ...snapshot.frames[0], url: 'about:blank' }] }), false)
})

test('initial normal document is not retried as a challenge when only http_custom is stale', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const snapshot = clearSnapshot('denied by http_custom')
  snapshot.frames[0].url = 'https://pay.ldxp.cn/'
  let drags = 0
  const manager = new ChallengeManager({
    enabled: true,
    sessionDirectory: directory,
    navigate: async () => ({}),
    inspect: async () => snapshot,
    providers: [{
      id: 'aliyun-esa',
      detect: () => true,
      locate: async () => sliderGeometry(),
    }],
    drag: async () => { drags += 1 },
  })

  const result = await manager.solve({ context: {}, page: {} })
  assert.deepEqual(result, { state: 'clear', provider: '' })
  assert.equal(drags, 0)
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
  assert.ok(trajectory.durationMilliseconds >= 900 && trajectory.durationMilliseconds <= 1_600)
  assert.ok(trajectory.travelDurationMilliseconds >= 600)
  assert.ok(trajectory.settleDurationMilliseconds >= 180)
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
  assert.equal(
    trajectory.travelDurationMilliseconds + trajectory.settleDurationMilliseconds,
    trajectory.durationMilliseconds,
  )
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

test('runtime API challenge is staged at its original URL and solved without an initial home navigation', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const apiURL = 'https://pay.ldxp.cn/shopApi/Shop/info'
  const challengeHTML = '<html><script src="/aliyuncaptcha.js"></script><div id="captcha-element">Please slide to verify</div></html>'
  let routeHandler
  let stagedResponse
  let inspected = 0
  let homeNavigations = 0
  let drags = 0
  const page = {
    route: async (url, handler, options) => {
      assert.equal(url, apiURL)
      assert.deepEqual(options, { times: 1 })
      routeHandler = handler
    },
    goto: async (url) => {
      assert.equal(url, apiURL)
      await routeHandler({ fulfill: async (response) => { stagedResponse = response } })
      return { headers: async () => stagedResponse.headers }
    },
    unroute: async (url, handler) => {
      assert.equal(url, apiURL)
      assert.equal(handler, routeHandler)
    },
    waitForNavigation: async () => ({}),
  }
  const manager = new ChallengeManager({
    enabled: true,
    sessionDirectory: directory,
    providers: [{
      id: 'aliyun-esa',
      detect: (snapshot) => snapshot.frames[0].hasAliyunDOM,
      locate: async () => sliderGeometry(),
    }],
    navigate: async () => { homeNavigations += 1; return {} },
    inspect: async () => {
      inspected += 1
      if (inspected === 1) {
        assert.equal(stagedResponse.status, 403)
        assert.equal(stagedResponse.body, challengeHTML)
        assert.equal(stagedResponse.headers['x-tengine-error'], 'denied by http_custom')
        return aliyunSnapshot()
      }
      return clearSnapshot()
    },
    drag: async () => { drags += 1 },
  })

  const result = await manager.solve({
    context: { storageState: async () => ({ cookies: [], origins: [] }) },
    page,
    challengeResponse: {
      status: 403,
      url: apiURL,
      contentType: 'text/html; charset=utf-8',
      responseError: 'denied by http_custom',
      text: challengeHTML,
    },
  })

  assert.equal(result.state, 'solved')
  assert.equal(result.provider, 'aliyun-esa')
  assert.equal(drags, 1)
  assert.equal(homeNavigations, 0)
})

test('runtime API challenge staging rejects a URL outside the configured origin', async () => {
  await assert.rejects(() => stageChallengeResponse(
    {},
    'https://pay.ldxp.cn',
    { url: 'https://example.com/challenge', text: '<html>verify</html>' },
    1_000
  ), (error) => error instanceof ChallengeError
    && error.challengeState === 'failed'
    && /escaped the configured origin/.test(error.message))
})

test('challenge manager does not report solved when session persistence fails', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const events = []
  let inspectCalls = 0
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => sliderGeometry(),
  }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => ({}),
    inspect: async () => inspectCalls++ === 0 ? aliyunSnapshot() : clearSnapshot(),
    drag: async () => {},
  })
  manager.saveSession = async () => { throw new Error('disk full') }

  await assert.rejects(() => manager.solve({
    context: {},
    page: {},
    onState: (event) => events.push(event),
  }), (error) => error instanceof ChallengeError && error.challengeState === 'failed')
  assert.equal(events.at(-1).state, 'failed')
})

test('challenge manager waits for the ESA success navigation before issuing a home request', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const postDragNormalPage = clearSnapshot('denied by http_custom')
  postDragNormalPage.frames[0].url = 'https://pay.ldxp.cn/'
  postDragNormalPage.frames[0].hasGenericSlider = true
  const snapshots = [aliyunSnapshot(), postDragNormalPage]
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

test('challenge manager refreshes once when the ESA shell has not painted its slider yet', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  let locateCalls = 0
  let reloads = 0
  const snapshots = [aliyunSnapshot(), aliyunSnapshot(), clearSnapshot()]
  const provider = {
    id: 'aliyun-esa',
    detect: () => true,
    locate: async () => {
      locateCalls += 1
      return locateCalls === 1 ? null : sliderGeometry()
    },
  }
  const manager = new ChallengeManager({
    enabled: true,
    providers: [provider],
    sessionDirectory: directory,
    navigate: async () => ({}),
    reload: async () => { reloads += 1; return {} },
    inspect: async () => snapshots.shift(),
    drag: async () => {},
  })

  const result = await manager.solve({
    context: { storageState: async () => ({ cookies: [], origins: [] }) },
    page: {},
  })
  assert.equal(result.state, 'solved')
  assert.equal(locateCalls, 2)
  assert.equal(reloads, 1)
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

test('challenge navigation tolerates Firefox and Chromium superseded-navigation errors', async (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))

  for (const message of [
    'page.goto: NS_ERROR_ABORT (0x80004004)',
    'page.goto: net::ERR_ABORTED at https://pay.ldxp.cn/',
  ]) {
    let loadStateCalls = 0
    const manager = new ChallengeManager({
      enabled: true,
      sessionDirectory: directory,
      inspect: async () => clearSnapshot(),
    })
    const result = await manager.solve({
      context: {},
      page: {
        goto: async () => { throw new Error(message) },
        waitForLoadState: async () => { loadStateCalls += 1 },
      },
    })
    assert.equal(result.state, 'clear')
    assert.equal(loadStateCalls, 1)
  }
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
  const secondStates = []
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
  const second = manager.solve({
    context: {},
    page: {},
    signal: controller.signal,
    onState: (event) => secondStates.push(event.state),
  })
  controller.abort(new Error('lane stopped'))
  await assert.rejects(() => second, /lane stopped/)
  assert.deepEqual(secondStates, ['queued', 'cancelled'])
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

  let failedAttempts = 0
  let recoveries = 0
  await assert.rejects(() => retryFailedChallengeOperation(
    async () => {
      failedAttempts += 1
      throw new ChallengeError('failed', 'HTML challenge')
    },
    async () => { recoveries += 1 },
    { maxRecoveries: 2 }
  ), (error) => error instanceof ChallengeError
    && error.challengeState === 'failed'
    && error.restartLane === true
    && challengeBackoffMilliseconds(1, error.challengeState) === 15 * 60_000)
  assert.equal(failedAttempts, 3)
  assert.equal(recoveries, 2)
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

test('challenge proxy recovery propagates cancellation without touching fallbacks', async () => {
  const controller = new AbortController()
  const reason = new Error('job lease lost')
  let switches = 0
  await assert.rejects(() => recoverChallengeAcrossProxyPool({
    poolSize: 3,
    currentIndex: 0,
    signal: controller.signal,
    solveCurrent: async () => {
      controller.abort(reason)
      throw reason
    },
    switchTo: async () => { switches += 1 },
  }), (error) => error === reason)
  assert.equal(switches, 0)
})

test('operation retry stops when the proxy pool reports a lane restart', async () => {
  let attempts = 0
  let recoveries = 0
  const terminal = new ChallengeError('failed', 'all exits rejected verification')
  terminal.restartLane = true

  await assert.rejects(() => retryFailedChallengeOperation(
    async () => {
      attempts += 1
      throw new ChallengeError('failed', 'HTML challenge')
    },
    async () => {
      recoveries += 1
      throw terminal
    }
  ), (error) => error === terminal && error.restartLane === true)

  assert.equal(attempts, 1)
  assert.equal(recoveries, 1)
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
    cookies: [{
      name: 'acw_sc__v3',
      value: 'passed',
      domain: '.ldxp.cn',
      path: '/',
      expires: 4_000_000_000,
    }],
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

test('expired Alibaba ESA session is ignored and removed before browser startup', (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const providers = [{ id: 'aliyun-esa' }]
  const origin = 'https://pay.ldxp.cn'
  const now = Date.UTC(2026, 7, 11)
  const storageState = {
    cookies: [{
      name: 'acw_sc__v3',
      value: 'expired',
      domain: '.ldxp.cn',
      path: '/',
      expires: (now / 1_000) - 1,
    }],
    origins: [],
  }
  const file = writeStoredSession(directory, providers[0].id, origin, null, storageState)

  assert.equal(readStoredSession(directory, providers, origin, null, now), null)
  assert.equal(fs.existsSync(file), false)
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

test('corrupt session state remains non-blocking when cleanup cannot remove the file', (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const providers = [{ id: 'aliyun-esa' }]
  const file = sessionFilePath(directory, providers[0].id, 'https://pay.ldxp.cn', null)
  fs.writeFileSync(file, '{broken')

  const originalRemove = fs.rmSync
  fs.rmSync = (target, options) => {
    if (target === file) {
      const error = new Error('read-only session directory')
      error.code = 'EACCES'
      throw error
    }
    return originalRemove(target, options)
  }
  try {
    assert.doesNotThrow(() => readStoredSession(directory, providers, 'https://pay.ldxp.cn', null))
    assert.equal(fs.existsSync(file), true)
  } finally {
    fs.rmSync = originalRemove
  }
})

test('structurally invalid cookie and origin state is removed before Playwright restore', (t) => {
  const directory = temporaryDirectory()
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const providers = [{ id: 'aliyun-esa' }]
  const file = sessionFilePath(directory, providers[0].id, 'https://pay.ldxp.cn', null)
  const invalidStates = [
    { cookies: [{ name: 'esa', value: 'ok', domain: '.ldxp.cn' }], origins: [] },
    { cookies: [{ name: 'esa', value: 'ok', domain: '.ldxp.cn', path: '/', sameSite: 'invalid' }], origins: [] },
    { cookies: [], origins: [{ origin: 'not-an-origin', localStorage: [] }] },
  ]

  for (const state of invalidStates) {
    fs.writeFileSync(file, JSON.stringify(state))
    assert.equal(readStoredSession(directory, providers, 'https://pay.ldxp.cn', null), null)
    assert.equal(fs.existsSync(file), false)
  }
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
