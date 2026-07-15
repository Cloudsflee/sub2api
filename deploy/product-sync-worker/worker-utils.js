const supportedProxyProtocols = new Set(['http:', 'https:', 'socks5:'])

function parsePositiveMilliseconds(value, fallback, name) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    if (value === undefined || value === null || value === '') return fallback
    throw new Error(`${name} must be a positive number`)
  }
  return Math.floor(parsed)
}

function parseSyncConcurrency(value, fallback = 3) {
  if (value === undefined || value === null || value === '') return fallback
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 5) {
    throw new Error('PRODUCT_SYNC_CONCURRENCY must be an integer between 1 and 5')
  }
  return parsed
}

function parseProxyConfiguration(value) {
  const raw = String(value || '').trim()
  if (!raw) return null

  let parsed
  try {
    parsed = new URL(raw)
  } catch {
    throw new Error('PRODUCT_SYNC_PROXY_URL must be a valid URL')
  }
  if (!supportedProxyProtocols.has(parsed.protocol)) {
    throw new Error('PRODUCT_SYNC_PROXY_URL must use http, https, or socks5')
  }
  if (!parsed.hostname || (parsed.pathname !== '' && parsed.pathname !== '/') || parsed.search || parsed.hash) {
    throw new Error('PRODUCT_SYNC_PROXY_URL must contain only proxy credentials, host, and port')
  }
  const username = decodeURIComponent(parsed.username)
  const password = decodeURIComponent(parsed.password)
  if (parsed.protocol === 'socks5:' && (username || password)) {
    throw new Error('authenticated SOCKS5 proxies are not supported; use an HTTP/HTTPS proxy')
  }
  return {
    server: `${parsed.protocol}//${parsed.host}`,
    username,
    password,
  }
}

function isVerificationPageState(state) {
  if (!state) return false
  if (state.hasCaptcha) return true
  const value = `${state.title || ''}\n${state.text || ''}`.toLowerCase()
  return value.includes('verification')
    || value.includes('please slide to verify')
    || value.includes('verify that you are a real person')
    || value.includes('滑动验证')
}

module.exports = {
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseSyncConcurrency,
}
