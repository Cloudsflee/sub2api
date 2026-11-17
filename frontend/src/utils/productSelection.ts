import type { SelectionData, SelectionResearchProfile, SelectionResearchSample } from '@/api/productSelections'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import { publicProductUnitPrice } from '@/utils/publicProductCatalog'

export function emptySelection(): SelectionData {
  return {
    title: '', product_id: '', shop_id: '', shop_name: '', source_url: '', category: '', research_id: '',
    stage: 'review', channels: [], tags: [], purchase_price: null, price_basis: 'pending', quote_verified_at: '',
    minimum_quantity: 1, target_sale_price: null, observed_sale_price: null, specification: '', customer: '',
    official_comparison: '', alternatives: '', information_gap: '', delivery_risk: '', acquisition_plan: '',
    economics: '', repeat_assessment: '', decision: '', next_action: '', notes: '',
  }
}

export function selectionFromProduct(product: PublicAccountImportProduct): SelectionData {
  const cost = publicProductUnitPrice(product)
  // A pending quote must never fall back to a base price or a stale payable price.
  const verified = !product.quote_pending && cost !== null && cost > 0
    && Boolean(product.quote_verified_at) && Number.isFinite(Date.parse(product.quote_verified_at || ''))
  return {
    ...emptySelection(), title: product.name, product_id: product.id, shop_id: product.shop_id,
    shop_name: product.shop_name, source_url: product.url, category: product.category || '',
    purchase_price: verified ? cost : null, price_basis: verified ? 'snapshot' : 'pending',
    quote_verified_at: verified ? product.quote_verified_at! : '', minimum_quantity: Math.max(1, product.minimum_quantity || 1),
  }
}

export function selectionFromResearch(profile: SelectionResearchProfile, sample?: SelectionResearchSample): SelectionData {
  return {
    ...emptySelection(), title: sample?.title || profile.name, product_id: sample?.id || '',
    shop_id: sample?.shop_id || '', shop_name: sample?.shop_name || '', source_url: sample?.source_url || '',
    category: profile.name, research_id: profile.id, stage: profile.stage,
    purchase_price: sample?.purchase_price ?? null, price_basis: sample ? 'snapshot' : 'pending',
    quote_verified_at: sample?.quote_verified_at || '', minimum_quantity: sample?.minimum_quantity || 1,
    specification: profile.subtypes, customer: profile.customer,
    official_comparison: [profile.competition, ...profile.references.map(r => `${r.title}：${r.url}`)].join('\n'),
    information_gap: profile.information, delivery_risk: profile.operations,
    acquisition_plan: profile.channels.join('\n'), economics: profile.economics,
    repeat_assessment: profile.repeat, decision: profile.position, next_action: profile.test,
  }
}

export function selectionErrorStatus(error: unknown): number | undefined {
  if (typeof error !== 'object' || !error) return undefined
  return (error as { status?: number; response?: { status?: number } }).status
    ?? (error as { response?: { status?: number } }).response?.status
}
