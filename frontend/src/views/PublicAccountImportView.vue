<template>
  <div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-white">
    <header class="border-b border-gray-200 bg-white dark:border-dark-800 dark:bg-dark-900">
      <div class="mx-auto flex h-16 max-w-6xl items-center justify-between px-4 sm:px-6">
        <div class="flex min-w-0 items-center gap-3">
          <img
            :src="siteLogo"
            alt="Logo"
            class="h-9 w-9 shrink-0 rounded-lg object-contain"
          />
          <span class="truncate text-base font-semibold">{{ siteName }}</span>
        </div>
        <RouterLink
          to="/login"
          class="inline-flex items-center gap-2 text-sm font-medium text-gray-600 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-400"
        >
          <Icon name="login" size="sm" />
          {{ t('publicAccountImport.adminLogin') }}
        </RouterLink>
      </div>
    </header>

    <main class="mx-auto max-w-5xl px-4 py-10 sm:px-6">
      <div class="mb-6">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('publicAccountImport.title') }}
        </h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
          {{ t('publicAccountImport.subtitle') }}
        </p>
      </div>

      <form
        class="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900"
        @submit.prevent="handleSubmit"
      >
        <div class="grid lg:grid-cols-[minmax(0,1.35fr)_minmax(280px,0.65fr)]">
          <section class="p-5 sm:p-6">
            <div class="mb-3 flex items-center justify-between gap-3">
              <label class="text-sm font-semibold">
                {{ t('publicAccountImport.filesLabel') }}
              </label>
              <span class="text-xs text-gray-500 dark:text-dark-400">
                {{ t('publicAccountImport.fileLimit') }}
              </span>
            </div>

            <button
              type="button"
              class="flex w-full flex-col items-center justify-center border border-dashed px-5 py-10 text-center transition-colors"
              :class="dragActive
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-950/30 dark:text-primary-300'
                : 'border-gray-300 bg-gray-50 text-gray-600 hover:border-primary-400 hover:bg-primary-50/40 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-300 dark:hover:border-primary-600'"
              :disabled="submitting"
              @click="openFilePicker"
              @dragenter.prevent="handleDragEnter"
              @dragover.prevent
              @dragleave.prevent="handleDragLeave"
              @drop.prevent="handleDrop"
            >
              <Icon name="upload" size="xl" class="mb-3" />
              <span class="text-sm font-medium">{{ t('publicAccountImport.dropFiles') }}</span>
              <span class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ t('publicAccountImport.chooseFiles') }}
              </span>
            </button>
            <input
              ref="fileInput"
              type="file"
              class="hidden"
              accept="application/json,.json"
              multiple
              @change="handleFileChange"
            />

            <div v-if="files.length" class="mt-4 border-y border-gray-200 dark:border-dark-700">
              <div
                v-for="(file, index) in files"
                :key="`${file.name}-${file.size}-${index}`"
                class="flex items-center gap-3 border-b border-gray-100 py-3 last:border-b-0 dark:border-dark-800"
              >
                <Icon name="document" size="sm" class="shrink-0 text-gray-400" />
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium" :title="file.name">{{ file.name }}</div>
                  <div class="text-xs text-gray-500 dark:text-dark-400">{{ formatBytes(file.size) }}</div>
                </div>
                <button
                  type="button"
                  class="p-1.5 text-gray-400 hover:text-red-600 dark:hover:text-red-400"
                  :title="t('publicAccountImport.removeFile')"
                  :aria-label="t('publicAccountImport.removeFile')"
                  :disabled="submitting"
                  @click="removeFile(index)"
                >
                  <Icon name="x" size="sm" />
                </button>
              </div>
            </div>
          </section>

          <section class="border-t border-gray-200 p-5 dark:border-dark-700 sm:p-6 lg:border-l lg:border-t-0">
            <div class="mb-3 flex items-center justify-between gap-3">
              <label class="text-sm font-semibold">
                {{ t('publicAccountImport.groupsLabel') }}
              </label>
              <span v-if="selectedGroupIds.length" class="text-xs font-medium text-primary-600 dark:text-primary-400">
                {{ t('publicAccountImport.selectedGroups', { count: selectedGroupIds.length }) }}
              </span>
            </div>

            <div v-if="loadingGroups" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
              {{ t('publicAccountImport.loadingGroups') }}
            </div>
            <div v-else-if="groups.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
              {{ t('publicAccountImport.noGroups') }}
            </div>
            <div v-else class="border-y border-gray-200 dark:border-dark-700">
              <label
                v-for="group in groups"
                :key="group.id"
                class="flex cursor-pointer items-center gap-3 border-b border-gray-100 py-3 last:border-b-0 dark:border-dark-800"
              >
                <input
                  v-model="selectedGroupIds"
                  type="checkbox"
                  :value="group.id"
                  :disabled="submitting"
                  class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
                />
                <span class="min-w-0 flex-1 truncate text-sm">{{ group.name }}</span>
              </label>
            </div>
          </section>
        </div>

        <div v-if="errorMessage" class="border-t border-red-200 bg-red-50 px-5 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300 sm:px-6">
          {{ errorMessage }}
        </div>

        <section v-if="result" class="border-t border-gray-200 dark:border-dark-700">
          <div class="px-5 py-4 sm:px-6">
            <div class="mb-3 flex items-center gap-2 text-sm font-semibold">
              <Icon name="checkCircle" size="sm" class="text-emerald-600 dark:text-emerald-400" />
              {{ t('publicAccountImport.resultTitle') }}
            </div>
            <div class="grid grid-cols-3 divide-x divide-gray-200 border-y border-gray-200 py-3 text-center dark:divide-dark-700 dark:border-dark-700">
              <div>
                <div class="text-lg font-semibold text-emerald-600 dark:text-emerald-400">{{ result.created }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountImport.created') }}</div>
              </div>
              <div>
                <div class="text-lg font-semibold text-amber-600 dark:text-amber-400">{{ result.skipped }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountImport.skipped') }}</div>
              </div>
              <div>
                <div class="text-lg font-semibold text-red-600 dark:text-red-400">{{ result.failed }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountImport.failed') }}</div>
              </div>
            </div>

            <div v-if="result.errors?.length" class="mt-4 space-y-1 text-xs text-red-700 dark:text-red-300">
              <div v-for="item in result.errors" :key="`error-${item.index}-${item.message}`">
                {{ item.name || `#${item.index}` }}: {{ item.message }}
              </div>
            </div>
            <div v-else-if="result.warnings?.length" class="mt-4 space-y-1 text-xs text-amber-700 dark:text-amber-300">
              <div v-for="item in result.warnings" :key="`warning-${item.index}-${item.message}`">
                {{ item.name || `#${item.index}` }}: {{ item.message }}
              </div>
            </div>
          </div>
        </section>

        <div class="flex items-center justify-end border-t border-gray-200 bg-gray-50 px-5 py-4 dark:border-dark-700 dark:bg-dark-800/60 sm:px-6">
          <button
            type="submit"
            class="btn btn-primary min-w-32"
            :disabled="submitting || loadingGroups || files.length === 0 || selectedGroupIds.length === 0"
          >
            <span v-if="submitting" class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white"></span>
            <Icon v-else name="upload" size="sm" class="mr-2" />
            {{ submitting ? t('publicAccountImport.submitting') : t('publicAccountImport.submit') }}
          </button>
        </div>
      </form>

      <div class="mt-10 flex border-b border-gray-200 dark:border-dark-700">
        <button
          type="button"
          class="border-b-2 px-5 py-3 text-sm font-semibold transition-colors"
          :class="activeCatalogTab === 'shops'
            ? 'border-primary-600 text-primary-700 dark:text-primary-300'
            : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-100'"
          @click="activeCatalogTab = 'shops'"
        >
          {{ t('publicAccountImport.shopModule') }}
        </button>
        <button
          type="button"
          class="border-b-2 px-5 py-3 text-sm font-semibold transition-colors"
          :class="activeCatalogTab === 'products'
            ? 'border-primary-600 text-primary-700 dark:text-primary-300'
            : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-100'"
          @click="activeCatalogTab = 'products'"
        >
          {{ t('publicAccountImport.productModule') }}
          <span v-if="products.length" class="ml-1 text-xs">({{ products.length }})</span>
        </button>
      </div>

      <section v-show="activeCatalogTab === 'shops'" class="pt-6">
        <div class="flex items-center justify-between gap-4">
          <h2 class="text-lg font-semibold">{{ t('publicAccountImport.shopsTitle') }}</h2>
          <span class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('publicAccountImport.shopsCount', { count: shops.length }) }}
          </span>
        </div>

        <form
          class="mt-4 grid gap-4 border-y border-gray-200 py-5 dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-[minmax(0,0.7fr)_minmax(0,1.3fr)_auto] lg:items-end"
          @submit.prevent="handleShopSubmit"
        >
          <div class="min-w-0">
            <label for="public-shop-name" class="input-label">
              {{ t('publicAccountImport.shopNameLabel') }}
            </label>
            <input
              id="public-shop-name"
              v-model="shopName"
              type="text"
              class="input"
              maxlength="80"
              autocomplete="organization"
              :placeholder="t('publicAccountImport.shopNamePlaceholder')"
              :disabled="submittingShop"
              @input="clearShopMessages"
            />
          </div>

          <div class="min-w-0">
            <label for="public-shop-url" class="input-label">
              {{ t('publicAccountImport.shopUrlLabel') }}
            </label>
            <input
              id="public-shop-url"
              v-model="shopUrl"
              type="url"
              class="input"
              maxlength="2048"
              inputmode="url"
              autocomplete="url"
              :placeholder="t('publicAccountImport.shopUrlPlaceholder')"
              :disabled="submittingShop"
              @input="clearShopMessages"
            />
          </div>

          <button
            type="submit"
            class="btn btn-primary h-10 whitespace-nowrap sm:col-span-2 lg:col-span-1"
            :disabled="submittingShop || !shopName.trim() || !shopUrl.trim()"
          >
            <span v-if="submittingShop" class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white"></span>
            <Icon v-else name="plus" size="sm" class="mr-2" />
            {{ submittingShop ? t('publicAccountImport.submittingShop') : t('publicAccountImport.submitShop') }}
          </button>
        </form>

        <div v-if="shopErrorMessage" class="border-b border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
          {{ shopErrorMessage }}
        </div>
        <div v-else-if="shopNoticeMessage" class="border-b border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300">
          {{ shopNoticeMessage }}
        </div>

        <div v-if="loadingShops" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('publicAccountImport.loadingShops') }}
        </div>
        <div v-else-if="shops.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('publicAccountImport.noShops') }}
        </div>
        <div v-else class="divide-y divide-gray-200 border-b border-gray-200 dark:divide-dark-700 dark:border-dark-700">
          <a
            v-for="shop in pagedShops"
            :key="shop.id"
            :href="shopHref(shop.url)"
            target="_blank"
            rel="noopener noreferrer nofollow ugc"
            class="flex min-h-16 items-center gap-3 px-1 py-3 text-gray-800 transition-colors hover:bg-gray-100 hover:text-primary-700 dark:text-dark-100 dark:hover:bg-dark-800 dark:hover:text-primary-300 sm:px-3"
            :title="t('publicAccountImport.visitShop')"
          >
            <Icon name="link" size="sm" class="shrink-0 text-gray-400" />
            <span class="min-w-0 flex-1">
              <span class="block break-words text-sm font-medium">{{ shop.name }}</span>
              <span class="block break-all text-xs text-gray-500 dark:text-dark-400">
                {{ shop.url }}
              </span>
            </span>
            <Icon name="externalLink" size="sm" class="shrink-0 text-gray-400" />
          </a>
        </div>

        <div v-if="shopPageCount > 1" class="flex items-center justify-between py-4 text-sm">
          <button type="button" class="btn btn-secondary" :disabled="shopPage <= 1" @click="shopPage--">
            {{ t('publicAccountImport.previousPage') }}
          </button>
          <span class="text-gray-500 dark:text-dark-400">
            {{ t('publicAccountImport.pageStatus', { page: shopPage, total: shopPageCount }) }}
          </span>
          <button type="button" class="btn btn-secondary" :disabled="shopPage >= shopPageCount" @click="shopPage++">
            {{ t('publicAccountImport.nextPage') }}
          </button>
        </div>
      </section>

      <section v-show="activeCatalogTab === 'products'" class="pt-6">
        <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h2 class="text-lg font-semibold">{{ t('publicAccountImport.productsTitle') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('publicAccountImport.productsHint') }}
              <span v-if="pendingProductShops > 0">
                · {{ t('publicAccountImport.productsSyncing', { count: pendingProductShops }) }}
              </span>
            </p>
          </div>
          <div class="w-full sm:w-80">
            <label for="public-product-search" class="input-label">
              {{ t('publicAccountImport.productSearchLabel') }}
            </label>
            <input
              id="public-product-search"
              v-model="productSearch"
              type="search"
              class="input"
              :placeholder="t('publicAccountImport.productSearchPlaceholder')"
            />
          </div>
        </div>

        <div v-if="loadingProducts" class="py-10 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('publicAccountImport.loadingProducts') }}
        </div>
        <div v-else-if="productErrorMessage" class="mt-5 border-y border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
          {{ productErrorMessage }}
        </div>
        <div v-else-if="filteredProducts.length === 0" class="py-10 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('publicAccountImport.noProducts') }}
        </div>
        <div v-else class="mt-5 grid gap-4 sm:grid-cols-2">
          <a
            v-for="product in pagedProducts"
            :key="product.id"
            :href="shopHref(product.url)"
            target="_blank"
            rel="noopener noreferrer nofollow"
            class="flex min-h-32 gap-4 rounded-lg border border-gray-200 bg-white p-4 transition hover:border-primary-300 hover:shadow-sm dark:border-dark-700 dark:bg-dark-900 dark:hover:border-primary-700"
          >
            <img
              v-if="product.image"
              :src="shopHref(product.image)"
              alt=""
              class="h-20 w-20 shrink-0 rounded-md bg-gray-100 object-cover dark:bg-dark-800"
              loading="lazy"
              referrerpolicy="no-referrer"
            />
            <div class="min-w-0 flex-1">
              <div class="line-clamp-2 text-sm font-semibold text-gray-900 dark:text-white">{{ product.name }}</div>
              <div class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400">
                {{ product.shop_name }}<span v-if="product.category"> · {{ product.category }}</span>
              </div>
              <div class="mt-3 flex items-end justify-between gap-3">
                <div>
                  <span class="text-lg font-bold text-red-600 dark:text-red-400">¥{{ formatPrice(product.price) }}</span>
                  <span v-if="product.market_price && product.market_price > product.price" class="ml-2 text-xs text-gray-400 line-through">
                    ¥{{ formatPrice(product.market_price) }}
                  </span>
                </div>
                <span class="shrink-0 text-xs text-gray-500 dark:text-dark-400">
                  {{ t('publicAccountImport.productStock', { count: product.stock }) }}
                </span>
              </div>
            </div>
          </a>
        </div>

        <div v-if="productPageCount > 1" class="flex items-center justify-between py-5 text-sm">
          <button type="button" class="btn btn-secondary" :disabled="productPage <= 1" @click="productPage--">
            {{ t('publicAccountImport.previousPage') }}
          </button>
          <span class="text-gray-500 dark:text-dark-400">
            {{ t('publicAccountImport.pageStatus', { page: productPage, total: productPageCount }) }}
          </span>
          <button type="button" class="btn btn-secondary" :disabled="productPage >= productPageCount" @click="productPage++">
            {{ t('publicAccountImport.nextPage') }}
          </button>
        </div>
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import {
  getPublicAccountImportGroups,
  getPublicAccountImportProducts,
  getPublicAccountImportShops,
  submitPublicAccountImport,
  submitPublicAccountImportShop,
  type PublicAccountImportGroup,
  type PublicAccountImportProduct,
  type PublicAccountImportResult,
  type PublicAccountImportShop,
} from '@/api/publicAccountImport'
import { sanitizeUrl } from '@/utils/url'

const MAX_FILE_BYTES = 512 * 1024
const CATALOG_PAGE_SIZE = 10

const { t } = useI18n()
const appStore = useAppStore()
const fileInput = ref<HTMLInputElement | null>(null)
const files = ref<File[]>([])
const groups = ref<PublicAccountImportGroup[]>([])
const selectedGroupIds = ref<number[]>([])
const loadingGroups = ref(true)
const submitting = ref(false)
const dragDepth = ref(0)
const errorMessage = ref('')
const result = ref<PublicAccountImportResult | null>(null)
const idempotencyKey = ref(createIdempotencyKey())
const shops = ref<PublicAccountImportShop[]>([])
const loadingShops = ref(true)
const submittingShop = ref(false)
const shopName = ref('')
const shopUrl = ref('')
const shopErrorMessage = ref('')
const shopNoticeMessage = ref('')
const activeCatalogTab = ref<'shops' | 'products'>('shops')
const shopPage = ref(1)
const products = ref<PublicAccountImportProduct[]>([])
const loadingProducts = ref(true)
const productErrorMessage = ref('')
const pendingProductShops = ref(0)
const productSearch = ref('')
const productPage = ref(1)
let shopRefreshTimer: number | undefined
let productRefreshTimer: number | undefined

const dragActive = computed(() => dragDepth.value > 0)
const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '/logo.png', { allowRelative: true, allowDataUrl: true }))
const shopPageCount = computed(() => Math.max(1, Math.ceil(shops.value.length / CATALOG_PAGE_SIZE)))
const pagedShops = computed(() => {
  const start = (shopPage.value - 1) * CATALOG_PAGE_SIZE
  return shops.value.slice(start, start + CATALOG_PAGE_SIZE)
})
const filteredProducts = computed(() => {
  const keyword = productSearch.value.trim().toLocaleLowerCase()
  const available = products.value.filter((product) => product.stock > 0)
  const matched = keyword
    ? available.filter((product) => [product.name, product.shop_name, product.category, product.goods_type]
        .some((value) => value?.toLocaleLowerCase().includes(keyword)))
    : available
  return [...matched].sort((a, b) => b.price - a.price || a.name.localeCompare(b.name))
})
const productPageCount = computed(() => Math.max(1, Math.ceil(filteredProducts.value.length / CATALOG_PAGE_SIZE)))
const pagedProducts = computed(() => {
  const start = (productPage.value - 1) * CATALOG_PAGE_SIZE
  return filteredProducts.value.slice(start, start + CATALOG_PAGE_SIZE)
})

onMounted(async () => {
  void appStore.fetchPublicSettings()
  void loadPublicShops(true)
  void loadPublicProducts(true)
  shopRefreshTimer = window.setInterval(() => void loadPublicShops(false), 30_000)
  productRefreshTimer = window.setInterval(() => void loadPublicProducts(false), 10_000)
  try {
    groups.value = await getPublicAccountImportGroups()
  } catch (error: any) {
    errorMessage.value = error?.message || t('publicAccountImport.loadFailed')
  } finally {
    loadingGroups.value = false
  }
})

onBeforeUnmount(() => {
  if (shopRefreshTimer !== undefined) window.clearInterval(shopRefreshTimer)
  if (productRefreshTimer !== undefined) window.clearInterval(productRefreshTimer)
})

watch(selectedGroupIds, resetSubmissionState, { deep: true })
watch(productSearch, () => { productPage.value = 1 })
watch(shopPageCount, (count) => { shopPage.value = Math.min(shopPage.value, count) })
watch(productPageCount, (count) => { productPage.value = Math.min(productPage.value, count) })

function createIdempotencyKey(): string {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  return `public-import-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function resetSubmissionState() {
  result.value = null
  errorMessage.value = ''
  idempotencyKey.value = createIdempotencyKey()
}

function openFilePicker() {
  fileInput.value?.click()
}

function handleFileChange(event: Event) {
  const target = event.target as HTMLInputElement
  setFiles(target.files)
  target.value = ''
}

function handleDragEnter() {
  if (!submitting.value) dragDepth.value += 1
}

function handleDragLeave() {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

function handleDrop(event: DragEvent) {
  dragDepth.value = 0
  if (!submitting.value) setFiles(event.dataTransfer?.files)
}

function setFiles(source: FileList | null | undefined) {
  const incoming = Array.from(source || [])
  if (!incoming.length) return
  const invalid = incoming.find((file) => !file.name.toLowerCase().endsWith('.json'))
  if (invalid) {
    errorMessage.value = t('publicAccountImport.jsonOnly')
    return
  }
  const oversized = incoming.find((file) => file.size > MAX_FILE_BYTES)
  if (oversized) {
    errorMessage.value = t('publicAccountImport.fileTooLarge', { name: oversized.name })
    return
  }
  files.value = incoming
  resetSubmissionState()
}

function removeFile(index: number) {
  files.value.splice(index, 1)
  resetSubmissionState()
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  return `${(bytes / 1024).toFixed(1)} KB`
}

async function handleSubmit() {
  if (!files.value.length) {
    errorMessage.value = t('publicAccountImport.selectFilesError')
    return
  }
  if (!selectedGroupIds.value.length) {
    errorMessage.value = t('publicAccountImport.selectGroupsError')
    return
  }

  submitting.value = true
  errorMessage.value = ''
  result.value = null
  try {
    const contents: string[] = []
    for (const file of files.value) {
      const content = (await file.text()).replace(/^\uFEFF/, '').trim()
      if (!content) throw new Error(t('publicAccountImport.emptyFile', { name: file.name }))
      contents.push(content)
    }

    result.value = await submitPublicAccountImport(
      { contents, group_ids: selectedGroupIds.value },
      idempotencyKey.value
    )
    idempotencyKey.value = createIdempotencyKey()
  } catch (error: any) {
    errorMessage.value = error?.message || t('publicAccountImport.importFailed')
  } finally {
    submitting.value = false
  }
}

async function loadPublicShops(showLoading: boolean) {
  if (showLoading) loadingShops.value = true
  try {
    shops.value = await getPublicAccountImportShops()
    if (showLoading) shopErrorMessage.value = ''
  } catch (error: any) {
    if (showLoading) {
      shopErrorMessage.value = error?.message || t('publicAccountImport.shopLoadFailed')
    }
  } finally {
    if (showLoading) loadingShops.value = false
  }
}

async function loadPublicProducts(showLoading: boolean) {
  if (showLoading) loadingProducts.value = true
  try {
    const catalog = await getPublicAccountImportProducts()
    products.value = catalog.products
    pendingProductShops.value = catalog.pending_shops
    productErrorMessage.value = ''
  } catch (error: any) {
    if (showLoading || products.value.length === 0) {
      productErrorMessage.value = error?.message || t('publicAccountImport.productLoadFailed')
    }
  } finally {
    if (showLoading) loadingProducts.value = false
  }
}

function formatPrice(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '')
}

function clearShopMessages() {
  shopErrorMessage.value = ''
  shopNoticeMessage.value = ''
}

function shopHref(value: string): string {
  return sanitizeUrl(value)
}

async function handleShopSubmit() {
  const name = shopName.value.trim()
  const url = shopUrl.value.trim()
  if (!name) {
    shopErrorMessage.value = t('publicAccountImport.shopNameRequired')
    return
  }
  if (!url) {
    shopErrorMessage.value = t('publicAccountImport.shopUrlRequired')
    return
  }

  submittingShop.value = true
  clearShopMessages()
  try {
    const submission = await submitPublicAccountImportShop({ name, url })
    shops.value = [submission.shop, ...shops.value.filter((shop) => shop.id !== submission.shop.id)]
    shopName.value = ''
    shopUrl.value = ''
    shopNoticeMessage.value = submission.created
      ? t('publicAccountImport.shopAdded')
      : t('publicAccountImport.shopAlreadyExists')
  } catch (error: any) {
    shopErrorMessage.value = error?.message || t('publicAccountImport.shopSubmitFailed')
  } finally {
    submittingShop.value = false
  }
}
</script>
