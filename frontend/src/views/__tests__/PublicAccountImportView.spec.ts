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

  it('exposes only per-shop refresh and does not restore full-catalog synchronization', () => {
    expect(source).not.toContain('@click="handleProductSync"')
    expect(source).toContain('requestPublicAccountImportProductRefresh(shop.id)')
    expect(source).toContain(':data-shop-product-refresh="shop.id"')
    expect(source).toContain('supportsPublicShopProductSync(shop.url)')
    expect(source).toContain("productPriceStatus(product.id) === 'checking'")
  })

  it('keeps the shop link separate from the product refresh button', () => {
    expect(source).toContain('<div\n            v-for="shop in pagedShops"')
    expect(source).toContain('@click="handleShopProductRefresh(shop)"')
    expect(source).toContain(':aria-label="shopProductRefreshLabel(shop.id)"')
    expect(source).toContain('shopProductRefreshRequests.value = { ...shopProductRefreshRequests.value, [shop.id]: true }')
  })

  it('preflights the minimum purchasable quantity before opening a product', () => {
    expect(source).toContain("'/shopApi/Shop/getUserChannel'")
    expect(source).toContain("'/shopApi/Shop/getGoodsPrice'")
    expect(source).toContain('quantity: snapshot.minimumQuantity')
    expect(source).toContain('markPublicProductUnavailable(product.id)')
  })

  it('provides exclusion syntax help beside the product search field', () => {
    expect(source).toContain('<HelpTooltip trigger="click"')
    expect(source).toContain('productSearchHelpDescription')
    expect(source).toContain('productSearchHelpExample')
  })

  it('separates queued shops from jobs that are actually running', () => {
    expect(source).toContain('queuedProductShops.value = catalog.queued_shops')
    expect(source).toContain('refreshingProductShops.value = catalog.refreshing_shops')
    expect(source).toContain('mergeShopProductSyncStatuses(catalog.shop_sync_statuses, true)')
    expect(source).toContain("productsRefreshingQueued")
    expect(source).toContain('productRefreshTimer = window.setInterval')
  })
})
