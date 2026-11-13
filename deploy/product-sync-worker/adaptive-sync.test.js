const assert = require('node:assert/strict')
const test = require('node:test')
const { collectCatalog, collectQuoteBatch, catalogMetadataVersion } = require('./catalog-sync')
const { Semaphore } = require('./worker-utils')

const product = { goods_key: 'one', name: 'One', link: 'https://wzyp.cn/item/one', goods_type: 'card', price: 2,
  status: 1, extend: { stock_count: 10, limit_count: 2 } }

test('complete catalogs include unavailable identities without any quote or detail requests', async () => {
  const calls = []
  const result = await collectCatalog({ shopToken: 'token', post: async (path, body) => {
    calls.push(path)
    if (path.endsWith('/info')) return { code: 1, data: {} }
    assert.ok(path.endsWith('/goodsList'))
    const list = body.goods_type === 'card' ? [product, { ...product, goods_key: 'off', status: 0 }] : []
    return { code: 1, data: { list, total: list.length } }
  } })
  assert.equal(calls.length, 5)
  assert.equal(result.complete, true)
  assert.equal(result.source_product_count, 2)
  assert.equal(result.products[0].sellable, true)
  assert.equal(result.products[1].sellable, false)
  assert.equal(result.products[1].goods_key, 'off')
  assert.equal(result.products[0].payable_price, undefined)
})

test('catalog refuses incomplete pagination, duplicate identities and >1000 without truncating', async () => {
  for (const [list, total, kind] of [[[], 1, 'unknown'], [[product, product], 2, 'unknown'], [[], 1001, 'catalog_limit']]) {
    await assert.rejects(collectCatalog({ shopToken: 't', post: async path => path.endsWith('/info')
      ? { code: 1, data: {} } : { code: 1, data: { list, total } } }), error => error.kind === kind)
  }
})

test('a quote batch reuses one payment channel and catalog data, retaining partial results on error', async () => {
  const calls = []
  const result = await collectQuoteBatch({ shopToken: 't', products: [1, 2, 3].map(i => ({ goods_key: String(i), minimum_quantity: 2, metadata_version: 'v' })),
    post: async (path, body) => {
      calls.push(path)
      if (path.endsWith('/getUserChannel')) return { code: 1, data: [{ id: 7 }] }
      assert.ok(path.endsWith('/getGoodsPrice'))
      assert.equal(body.channel_id, 7)
      assert.equal(body.quantity, 2)
      if (body.goods_key === '2') throw Object.assign(new Error('network failed'), { kind: 'network' })
      return { code: 1, data: { total_amount: 6 } }
    } })
  assert.equal(calls.length, 3)
  assert.equal(result.products.length, 1)
  assert.equal(result.products[0].payable_price, 6)
  assert.equal(result.failure_kind, 'network')
})

test('batch yields after 30 seconds finishing the current request and caps input at twenty', async () => {
  let time = 0
  const result = await collectQuoteBatch({ shopToken: 't', products: Array.from({ length: 20 }, (_, i) => ({ goods_key: String(i), minimum_quantity: 1 })), clock: () => time,
    post: async path => {
      if (path.endsWith('/getUserChannel')) return { code: 1, data: [{ id: 1 }] }
      time += 11_000
      return { code: 1, data: { total_amount: 1 } }
    } })
  assert.equal(result.products.length, 3)
  await assert.rejects(collectQuoteBatch({ products: Array(21).fill({}) }), /1-20/)
})

test('concurrent batches preserve the shared quote concurrency limit', async () => {
  const semaphore = new Semaphore(2)
  let active = 0; let maximum = 0
  const post = async path => {
    if (path.endsWith('/getUserChannel')) return { code: 1, data: [{ id: 1 }] }
    active++; maximum = Math.max(maximum, active)
    await new Promise(resolve => setTimeout(resolve, 2))
    active--
    return { code: 1, data: { total_amount: 1 } }
  }
  await Promise.all(Array.from({ length: 6 }, () => collectQuoteBatch({ shopToken: 't', products: [{ goods_key: 'x', minimum_quantity: 1 }], post, quoteSemaphore: semaphore })))
  assert.equal(maximum, 2)
})

test('quote condition digests ignore cosmetic changes and retain unknown pricing conditions', () => {
  assert.equal(catalogMetadataVersion(product), catalogMetadataVersion({ ...product, name: 'New name' }))
  assert.notEqual(catalogMetadataVersion(product), catalogMetadataVersion({ ...product, extend: { ...product.extend, limit_count: 3 } }))
  assert.notEqual(catalogMetadataVersion(product), catalogMetadataVersion({ ...product, tier_prices: [1, 2] }))
})
