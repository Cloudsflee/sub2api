import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../PublicAccountImportView.vue'),
  'utf8'
)

describe('PublicAccountImportView product synchronization', () => {
  it('loads cached products without claiming sync jobs or checking visible prices on mount', () => {
    expect(source).toContain('void loadPublicProducts(true)')
    expect(source).not.toContain('syncNextPublicShop')
    expect(source).not.toContain('verifyVisiblePublicProducts')
    expect(source).not.toContain("['idle', 'checking'].includes(productPriceStatus(product.id))")
  })

  it('does not expose a manual full-catalog synchronization action', () => {
    expect(source).not.toContain('@click="handleProductSync"')
    expect(source).not.toContain('requestPublicAccountImportProductRefresh')
    expect(source).toContain("productPriceStatus(product.id) === 'checking'")
  })

  it('separates queued shops from jobs that are actually running', () => {
    expect(source).toContain('queuedProductShops.value = catalog.queued_shops')
    expect(source).toContain('refreshingProductShops.value = catalog.refreshing_shops')
    expect(source).toContain("productsRefreshingQueued")
    expect(source).toContain('productRefreshTimer = window.setInterval')
  })
})
