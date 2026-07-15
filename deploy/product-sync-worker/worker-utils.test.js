const assert = require('node:assert/strict')
const test = require('node:test')

const {
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseSyncConcurrency,
} = require('./worker-utils')

test('parseProxyConfiguration removes credentials from the Chromium proxy argument', () => {
  assert.deepEqual(parseProxyConfiguration('http://user:p%40ss@proxy.example:8080'), {
    server: 'http://proxy.example:8080',
    username: 'user',
    password: 'p@ss',
  })
})

test('parseProxyConfiguration supports an unauthenticated SOCKS5 proxy', () => {
  assert.deepEqual(parseProxyConfiguration('socks5://127.0.0.1:1080'), {
    server: 'socks5://127.0.0.1:1080',
    username: '',
    password: '',
  })
})

test('parseProxyConfiguration rejects unsafe or unsupported forms', () => {
  assert.throws(() => parseProxyConfiguration('ftp://proxy.example:21'), /must use http/)
  assert.throws(() => parseProxyConfiguration('http://proxy.example/path'), /only proxy credentials/)
  assert.throws(() => parseProxyConfiguration('socks5://user:pass@proxy.example:1080'), /authenticated SOCKS5/)
})

test('isVerificationPageState detects the Alibaba ESA challenge', () => {
  assert.equal(isVerificationPageState({
    title: 'Verification',
    text: 'Please slide to verify',
    hasCaptcha: true,
  }), true)
  assert.equal(isVerificationPageState({ title: 'Shop', text: 'Products', hasCaptcha: false }), false)
})

test('parsePositiveMilliseconds validates timing configuration', () => {
  assert.equal(parsePositiveMilliseconds(undefined, 20000, 'TIMEOUT'), 20000)
  assert.equal(parsePositiveMilliseconds('1500.9', 20000, 'TIMEOUT'), 1500)
  assert.throws(() => parsePositiveMilliseconds('0', 20000, 'TIMEOUT'), /positive number/)
})

test('parseSyncConcurrency defaults to three and enforces the worker limit', () => {
  assert.equal(parseSyncConcurrency(undefined), 3)
  assert.equal(parseSyncConcurrency('5'), 5)
  assert.throws(() => parseSyncConcurrency('0'), /between 1 and 5/)
  assert.throws(() => parseSyncConcurrency('2.5'), /between 1 and 5/)
  assert.throws(() => parseSyncConcurrency('6'), /between 1 and 5/)
})
