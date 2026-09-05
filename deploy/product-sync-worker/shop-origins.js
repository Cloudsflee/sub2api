const SHOP_CANONICAL_ORIGIN = 'https://wzyp.cn'
const SHOP_LEGACY_ORIGIN = 'https://pay.ldxp.cn'
const SHOP_TRUSTED_ORIGINS = Object.freeze([
  SHOP_CANONICAL_ORIGIN,
  SHOP_LEGACY_ORIGIN,
])

function isTrustedShopOrigin(value) {
  let origin
  try {
    origin = new URL(String(value || '')).origin
  } catch {
    return false
  }
  return SHOP_TRUSTED_ORIGINS.includes(origin)
}

function isTrustedShopHandoff(expectedOrigin, actualOrigin) {
  return expectedOrigin === actualOrigin
    || (isTrustedShopOrigin(expectedOrigin) && isTrustedShopOrigin(actualOrigin))
}

module.exports = {
  SHOP_CANONICAL_ORIGIN,
  SHOP_LEGACY_ORIGIN,
  SHOP_TRUSTED_ORIGINS,
  isTrustedShopHandoff,
  isTrustedShopOrigin,
}
