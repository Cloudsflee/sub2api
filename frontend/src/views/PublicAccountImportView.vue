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

      <div class="mb-6 grid grid-cols-3 border-b border-gray-200 dark:border-dark-700">
        <button
          v-for="tab in mainTabs"
          :key="tab.value"
          type="button"
          class="border-b-2 px-3 py-3 text-sm font-semibold transition-colors"
          :class="activeMainTab === tab.value
            ? 'border-primary-600 text-primary-700 dark:text-primary-300'
            : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-100'"
          @click="activeMainTab = tab.value"
        >
          {{ tab.label }}
          <span v-if="tab.value === 'products' && products.length" class="ml-1 text-xs">({{ products.length }})</span>
        </button>
      </div>

      <div
        v-show="activeMainTab === 'import'"
        class="mb-4 border-y border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200"
      >
        {{ t('publicAccountImport.k12PurchaseNotice') }}
      </div>

      <form
        v-show="activeMainTab === 'import'"
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
            <div class="grid grid-cols-4 divide-x divide-gray-200 border-y border-gray-200 py-3 text-center dark:divide-dark-700 dark:border-dark-700">
              <div>
                <div class="text-lg font-semibold text-emerald-600 dark:text-emerald-400">{{ result.created }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountImport.created') }}</div>
              </div>
              <div>
                <div class="text-lg font-semibold text-blue-600 dark:text-blue-400">{{ result.updated ?? 0 }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountImport.updated') }}</div>
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

            <div v-if="result.items?.length" class="mt-4">
              <div class="mb-2 text-xs font-semibold text-gray-600 dark:text-dark-300">
                {{ t('publicAccountImport.detailsTitle') }}
              </div>
              <div class="divide-y divide-gray-100 border-y border-gray-200 dark:divide-dark-800 dark:border-dark-700">
                <div
                  v-for="item in result.items"
                  :key="`item-${item.index}-${item.action}-${item.name || ''}`"
                  class="flex min-w-0 gap-3 py-2.5 text-xs"
                >
                  <span class="w-14 shrink-0 font-semibold" :class="importActionClass(item.action)">
                    {{ importActionLabel(item.action) }}
                  </span>
                  <div class="min-w-0 flex-1">
                    <div class="break-all font-medium text-gray-800 dark:text-dark-100">
                      {{ item.name || `#${item.index}` }}
                    </div>
                    <div v-if="item.message" class="mt-0.5 break-words text-gray-500 dark:text-dark-400">
                      {{ item.message }}
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div v-if="additionalErrors.length" class="mt-4 border-y border-red-200 bg-red-50 px-3 py-2.5 text-xs text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
              <div class="mb-1 font-semibold">{{ t('publicAccountImport.errorsTitle') }}</div>
              <div v-for="item in additionalErrors" :key="`error-${item.index}-${item.message}`" class="break-words">
                {{ item.name || `#${item.index}` }}: {{ item.message }}
              </div>
            </div>
            <div v-if="additionalWarnings.length" class="mt-4 border-y border-amber-200 bg-amber-50 px-3 py-2.5 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
              <div class="mb-1 font-semibold">{{ t('publicAccountImport.remindersTitle') }}</div>
              <div v-for="item in additionalWarnings" :key="`warning-${item.index}-${item.message}`" class="break-words">
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

      <section v-show="activeMainTab === 'shops'" class="pt-2">
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
          <div
            v-for="shop in pagedShops"
            :key="shop.id"
            class="flex min-h-20 items-center gap-2 px-1 py-3 sm:px-3"
          >
            <a
              :href="shopHref(shop.url)"
              target="_blank"
              rel="noopener noreferrer nofollow ugc"
              class="flex min-w-0 flex-1 items-center gap-3 text-gray-800 transition-colors hover:text-primary-700 dark:text-dark-100 dark:hover:text-primary-300"
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
            <div v-if="supportsPublicShopProductSync(shop.url)" class="flex shrink-0 items-center gap-2">
              <div class="w-28 text-right text-[11px] leading-4 text-gray-500 dark:text-dark-400 sm:w-36">
                <span class="block">{{ shopProductUpdatedText(shop.id) }}</span>
                <span
                  v-if="shopProductStateText(shop.id)"
                  class="block font-medium"
                  :class="shopProductStateClass(shop.id)"
                >
                  {{ shopProductStateText(shop.id) }}
                </span>
              </div>
              <button
                type="button"
                class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-md border border-gray-300 text-gray-600 transition-colors hover:border-primary-400 hover:bg-primary-50 hover:text-primary-700 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:text-dark-300 dark:hover:border-primary-600 dark:hover:bg-dark-800 dark:hover:text-primary-300"
                :disabled="shopProductRefreshIsDisabled(shop.id)"
                :title="shopProductRefreshLabel(shop.id)"
                :aria-label="shopProductRefreshLabel(shop.id)"
                :data-shop-product-refresh="shop.id"
                @click="handleShopProductRefresh(shop)"
              >
                <Icon
                  name="refresh"
                  size="sm"
                  :class="{ 'animate-spin': shopProductRefreshIsSpinning(shop.id) }"
                />
              </button>
            </div>
          </div>
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

      <section v-show="activeMainTab === 'products'" class="pt-2">
        <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div class="flex flex-wrap items-start gap-3">
            <div>
              <h2 class="text-lg font-semibold">{{ t('publicAccountImport.productsTitle') }}</h2>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ t('publicAccountImport.productsHint') }}
                <span v-if="refreshingProductShops > 0 && queuedProductShops > 0">
                  · {{ t('publicAccountImport.productsRefreshingQueued', {
                    refreshing: refreshingProductShops,
                    queued: queuedProductShops,
                  }) }}
                </span>
                <span v-else-if="refreshingProductShops > 0">
                  · {{ t('publicAccountImport.productsRefreshing', { count: refreshingProductShops }) }}
                </span>
                <span v-else-if="queuedProductShops > 0">
                  · {{ t('publicAccountImport.productsQueued', { count: queuedProductShops }) }}
                </span>
                <span v-if="failedProductShops > 0">
                  · {{ t('publicAccountImport.productsFailed', { count: failedProductShops }) }}
                </span>
								<span v-if="expiredProductShops > 0">
									· {{ t('publicAccountImport.productsExpired', { count: expiredProductShops }) }}
								</span>
              </p>
            </div>
          </div>
          <div class="w-full sm:w-80">
            <div class="mb-1.5 flex items-center">
              <label for="public-product-search" class="input-label mb-0">
                {{ t('publicAccountImport.productSearchLabel') }}
              </label>
              <HelpTooltip trigger="click" width-class="w-40 sm:w-72">
                <template #trigger>
                  <button
                    type="button"
                    class="inline-flex h-5 w-5 items-center justify-center rounded-full text-gray-400 transition-colors hover:text-primary-600 focus:outline-none focus:ring-2 focus:ring-primary-500/30 dark:text-dark-400 dark:hover:text-primary-400"
                    :aria-label="t('publicAccountImport.productSearchHelpLabel')"
                    :title="t('publicAccountImport.productSearchHelpLabel')"
                  >
                    <Icon name="questionCircle" size="sm" />
                  </button>
                </template>
                <div class="space-y-2">
                  <p class="font-medium text-white">
                    {{ t('publicAccountImport.productSearchHelpTitle') }}
                  </p>
                  <p class="text-gray-200">
                    {{ t('publicAccountImport.productSearchHelpDescription') }}
                  </p>
                  <code class="block rounded bg-white/10 px-2 py-1.5 font-mono text-[11px] text-white">
                    {{ t('publicAccountImport.productSearchHelpExample') }}
                  </code>
                </div>
              </HelpTooltip>
            </div>
            <input
              id="public-product-search"
              v-model="productSearch"
              type="search"
              class="input"
              :placeholder="t('publicAccountImport.productSearchPlaceholder')"
            />
            <div class="mt-2 grid grid-cols-2 overflow-hidden rounded-md border border-gray-200 dark:border-dark-700">
              <button
                type="button"
                class="px-3 py-2 text-xs font-medium transition-colors"
                :class="productPriceOrder === 'desc'
                  ? 'bg-primary-600 text-white'
                  : 'bg-white text-gray-600 hover:bg-gray-50 dark:bg-dark-900 dark:text-dark-300 dark:hover:bg-dark-800'"
                :aria-pressed="productPriceOrder === 'desc'"
                @click="productPriceOrder = 'desc'"
              >
                {{ t('publicAccountImport.priceDescending') }}
              </button>
              <button
                type="button"
                class="border-l border-gray-200 px-3 py-2 text-xs font-medium transition-colors dark:border-dark-700"
                :class="productPriceOrder === 'asc'
                  ? 'bg-primary-600 text-white'
                  : 'bg-white text-gray-600 hover:bg-gray-50 dark:bg-dark-900 dark:text-dark-300 dark:hover:bg-dark-800'"
                :aria-pressed="productPriceOrder === 'asc'"
                @click="productPriceOrder = 'asc'"
              >
                {{ t('publicAccountImport.priceAscending') }}
              </button>
            </div>
          </div>
        </div>

        <div v-if="productVerificationMessage" class="mt-5 border-y border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
          {{ productVerificationMessage }}
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
            :aria-busy="productPriceStatus(product.id) === 'checking'"
            @click="handleProductClick($event, product)"
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
              <div class="mt-1 text-xs text-gray-400 dark:text-dark-500" :title="product.updated_at">
                {{ t('publicAccountImport.productUpdatedAt', { time: formatProductUpdatedAt(product.updated_at) }) }}
                <span v-if="productPriceStatus(product.id) === 'verified'" class="text-emerald-600 dark:text-emerald-400">
                  · {{ t('publicAccountImport.priceVerified') }}
                </span>
                <span v-else-if="productPriceStatus(product.id) === 'failed'" class="text-amber-600 dark:text-amber-400">
                  · {{ t('publicAccountImport.priceCheckFailed') }}
                </span>
              </div>
              <div class="mt-3 flex items-end justify-between gap-3">
                <div>
                  <span v-if="productPriceStatus(product.id) === 'checking'" class="inline-flex h-7 items-center text-xs font-medium text-gray-500 dark:text-dark-400">
                    <span class="mr-2 h-3.5 w-3.5 animate-spin rounded-full border-2 border-gray-300 border-t-primary-600 dark:border-dark-600 dark:border-t-primary-400"></span>
                    {{ t('publicAccountImport.priceChecking') }}
                  </span>
                  <template v-else>
									<span class="text-lg font-bold text-red-600 dark:text-red-400">¥{{ formatPrice(publicProductPayablePrice(product)) }}</span>
									<span v-if="product.minimum_quantity === 1 && product.market_price && product.market_price > publicProductPayablePrice(product)" class="ml-2 text-xs text-gray-400 line-through">
                      ¥{{ formatPrice(product.market_price) }}
                    </span>
									<div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
										{{ t('publicAccountImport.productUnitPrice', { price: formatPrice(publicProductUnitPrice(product)) }) }}
										· {{ t('publicAccountImport.productMinimumQuantity', { count: product.minimum_quantity }) }}
									</div>
                  </template>
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
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { useAppStore } from '@/stores/app'
import {
  getPublicAccountImportGroups,
	getPublicAccountImportProductsWithETag,
  getPublicAccountImportShops,
  requestPublicAccountImportProductRefresh,
  submitPublicAccountImport,
  submitPublicAccountImportShop,
  type PublicAccountImportGroup,
  type PublicAccountImportProduct,
  type PublicAccountImportProductSyncStatus,
  type PublicAccountImportResult,
  type PublicAccountImportShop,
} from '@/api/publicAccountImport'
import {
  filterAndSortPublicProducts,
  livePublicProductAvailability,
	livePublicProductMinimumQuantity,
	livePublicProductQuoteAvailability,
	publicProductPayablePrice,
	publicProductGoodsKey,
	publicProductUnitPrice,
	selectLivePublicProductPaymentChannel,
} from '@/utils/publicProductCatalog'
import {
  publicShopProductRefreshDisabled,
  publicShopProductSyncRetryAfter,
  supportsPublicShopProductSync,
  trackPublicShopProductSyncStatus,
  type TrackedPublicAccountImportProductSyncStatus,
} from '@/utils/publicShopProductSync'
import { sanitizeUrl } from '@/utils/url'

const MAX_FILE_BYTES = 512 * 1024
const CATALOG_PAGE_SIZE = 10
const PRODUCT_PRICE_VERIFICATION_TTL_MS = 60_000
const PRODUCT_PRICE_FAILURE_RETRY_MS = 15_000
const PRODUCT_UNAVAILABLE_TTL_MS = 15 * 60_000

type ProductPriceStatus = 'checking' | 'verified' | 'unavailable' | 'failed'

interface ProductPriceVerification {
  status: ProductPriceStatus
  checkedAt: number
}

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
const activeMainTab = ref<'import' | 'shops' | 'products'>('import')
const shopPage = ref(1)
const products = ref<PublicAccountImportProduct[]>([])
const loadingProducts = ref(true)
const productErrorMessage = ref('')
const productVerificationMessage = ref('')
const productPriceVerifications = ref<Record<string, ProductPriceVerification>>({})
const queuedProductShops = ref(0)
const refreshingProductShops = ref(0)
const failedProductShops = ref(0)
const expiredProductShops = ref(0)
const shopProductSyncStatuses = ref<Record<string, TrackedPublicAccountImportProductSyncStatus>>({})
const shopProductRefreshRequests = ref<Record<string, boolean>>({})
const shopProductSyncClock = ref(Date.now())
const productSearch = ref('')
const productPriceOrder = ref<'desc' | 'asc'>('asc')
const productPage = ref(1)
let shopRefreshTimer: number | undefined
let productRefreshTimer: number | undefined
let shopProductSyncClockTimer: number | undefined
let productCatalogETag: string | null = null
let productCatalogRequestInFlight = false
let productCatalogMounted = false
const productVerificationPromises = new Map<string, Promise<boolean>>()

const dragActive = computed(() => dragDepth.value > 0)
const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '/logo.png', { allowRelative: true, allowDataUrl: true }))
const mainTabs = computed(() => [
  { value: 'import' as const, label: t('publicAccountImport.importModule') },
  { value: 'shops' as const, label: t('publicAccountImport.shopModule') },
  { value: 'products' as const, label: t('publicAccountImport.productModule') },
])
const shopPageCount = computed(() => Math.max(1, Math.ceil(shops.value.length / CATALOG_PAGE_SIZE)))
const pagedShops = computed(() => {
  const start = (shopPage.value - 1) * CATALOG_PAGE_SIZE
  return shops.value.slice(start, start + CATALOG_PAGE_SIZE)
})
const filteredProducts = computed(() => filterAndSortPublicProducts(
  products.value,
  productSearch.value,
	productPriceOrder.value
))
const productPageCount = computed(() => Math.max(1, Math.ceil(filteredProducts.value.length / CATALOG_PAGE_SIZE)))
const pagedProducts = computed(() => {
  const start = (productPage.value - 1) * CATALOG_PAGE_SIZE
  return filteredProducts.value.slice(start, start + CATALOG_PAGE_SIZE)
})
const additionalErrors = computed(() => filterAdditionalResultMessages(result.value?.errors || []))
const additionalWarnings = computed(() => filterAdditionalResultMessages(result.value?.warnings || []))

onMounted(async () => {
  productCatalogMounted = true
  void appStore.fetchPublicSettings()
  void loadPublicShops(true)
  void loadPublicProducts(true)
  shopRefreshTimer = window.setInterval(() => void loadPublicShops(false), 30_000)
	document.addEventListener('visibilitychange', handleProductCatalogVisibilityChange)
	window.addEventListener('focus', handleProductCatalogFocus)
  shopProductSyncClockTimer = window.setInterval(() => {
    shopProductSyncClock.value = Date.now()
  }, 1_000)
  try {
    groups.value = await getPublicAccountImportGroups()
  } catch (error: any) {
    errorMessage.value = error?.message || t('publicAccountImport.loadFailed')
  } finally {
    loadingGroups.value = false
  }
})

onBeforeUnmount(() => {
  productCatalogMounted = false
  if (shopRefreshTimer !== undefined) window.clearInterval(shopRefreshTimer)
	if (productRefreshTimer !== undefined) window.clearTimeout(productRefreshTimer)
  if (shopProductSyncClockTimer !== undefined) window.clearInterval(shopProductSyncClockTimer)
	document.removeEventListener('visibilitychange', handleProductCatalogVisibilityChange)
	window.removeEventListener('focus', handleProductCatalogFocus)
})

watch(selectedGroupIds, resetSubmissionState, { deep: true })
watch(productSearch, () => { productPage.value = 1 })
watch(productPriceOrder, () => { productPage.value = 1 })
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

function filterAdditionalResultMessages<T extends { index: number; message: string }>(messages: T[]): T[] {
  const items = result.value?.items || []
  return messages.filter((message) => !items.some((item) => (
    item.index === message.index && item.message === message.message
  )))
}

function importActionLabel(action: string): string {
  switch (action) {
    case 'created': return t('publicAccountImport.created')
    case 'updated': return t('publicAccountImport.updated')
    case 'skipped': return t('publicAccountImport.skipped')
    case 'failed': return t('publicAccountImport.failed')
    default: return action
  }
}

function importActionClass(action: string): string {
  switch (action) {
    case 'created': return 'text-emerald-600 dark:text-emerald-400'
    case 'updated': return 'text-blue-600 dark:text-blue-400'
    case 'skipped': return 'text-amber-600 dark:text-amber-400'
    case 'failed': return 'text-red-600 dark:text-red-400'
    default: return 'text-gray-500 dark:text-dark-400'
  }
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
	if (productCatalogRequestInFlight) return
	if (document.hidden) {
		if (showLoading) loadingProducts.value = false
		return
	}
	productCatalogRequestInFlight = true
  if (showLoading) loadingProducts.value = true
  try {
		const result = await getPublicAccountImportProductsWithETag(productCatalogETag)
		if (result.etag) productCatalogETag = result.etag
		if (result.notModified || !result.data) {
			productErrorMessage.value = ''
			return
		}
		const catalog = result.data
    const nextProducts = catalog.products
      .filter((product) => !recentProductVerification(product.id, 'unavailable'))
    products.value = nextProducts
    queuedProductShops.value = catalog.queued_shops
    refreshingProductShops.value = catalog.refreshing_shops
    failedProductShops.value = catalog.failed_shops
		expiredProductShops.value = catalog.expired_shops
    mergeShopProductSyncStatuses(catalog.shop_sync_statuses, true)
    productErrorMessage.value = ''
  } catch (error: any) {
    if (showLoading || products.value.length === 0) {
      productErrorMessage.value = error?.message || t('publicAccountImport.productLoadFailed')
    }
  } finally {
    if (showLoading) loadingProducts.value = false
		productCatalogRequestInFlight = false
		scheduleProductCatalogRefresh()
  }
}

function scheduleProductCatalogRefresh() {
	if (productRefreshTimer !== undefined) window.clearTimeout(productRefreshTimer)
	productRefreshTimer = undefined
	if (!productCatalogMounted || document.hidden) return
	const syncing = queuedProductShops.value > 0 || refreshingProductShops.value > 0
	productRefreshTimer = window.setTimeout(() => {
		productRefreshTimer = undefined
		void loadPublicProducts(false)
	}, syncing ? 10_000 : 60_000)
}

function handleProductCatalogVisibilityChange() {
	if (document.hidden) {
		if (productRefreshTimer !== undefined) window.clearTimeout(productRefreshTimer)
		productRefreshTimer = undefined
		return
	}
	void loadPublicProducts(false)
}

function handleProductCatalogFocus() {
	if (!document.hidden) void loadPublicProducts(false)
}

function formatPrice(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '')
}

function formatProductUpdatedAt(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

function mergeShopProductSyncStatuses(statuses: PublicAccountImportProductSyncStatus[], replace: boolean) {
  const now = Date.now()
  const next = replace ? {} : { ...shopProductSyncStatuses.value }
  for (const status of statuses) {
    if (!status.shop_id) continue
    next[status.shop_id] = trackPublicShopProductSyncStatus(status, now)
  }
  shopProductSyncStatuses.value = next
  shopProductSyncClock.value = now
}

function shopProductSyncStatus(shopId: string): TrackedPublicAccountImportProductSyncStatus | undefined {
  return shopProductSyncStatuses.value[shopId]
}

function shopProductRetryAfter(shopId: string): number {
  return publicShopProductSyncRetryAfter(shopProductSyncStatus(shopId), shopProductSyncClock.value)
}

function shopProductRefreshIsDisabled(shopId: string): boolean {
  return publicShopProductRefreshDisabled(
    shopProductSyncStatus(shopId),
    Boolean(shopProductRefreshRequests.value[shopId]),
    shopProductSyncClock.value
  )
}

function shopProductRefreshIsSpinning(shopId: string): boolean {
  return Boolean(shopProductRefreshRequests.value[shopId]) || shopProductSyncStatus(shopId)?.state === 'refreshing'
}

function shopProductUpdatedText(shopId: string): string {
	const status = shopProductSyncStatus(shopId)
	const updatedAt = status?.snapshot_updated_at || status?.updated_at
  return updatedAt
    ? t('publicAccountImport.shopProductsUpdatedAt', { time: formatProductUpdatedAt(updatedAt) })
    : t('publicAccountImport.shopProductsNeverUpdated')
}

function shopProductStateText(shopId: string): string {
  const status = shopProductSyncStatus(shopId)
  if (status?.state === 'queued') return t('publicAccountImport.shopProductsQueued')
  if (status?.state === 'refreshing') return t('publicAccountImport.shopProductsRefreshing')
  if (status?.state === 'failed') return t('publicAccountImport.shopProductsFailed')
	if (status?.snapshot_state === 'stale') return t('publicAccountImport.shopProductsStale')
	if (status?.snapshot_state === 'expired') return t('publicAccountImport.shopProductsExpired')
	if (status?.snapshot_state === 'legacy') return t('publicAccountImport.shopProductsLegacy')
  const retryAfter = shopProductRetryAfter(shopId)
  return retryAfter > 0
    ? t('publicAccountImport.shopProductsCooldown', { seconds: retryAfter })
    : ''
}

function shopProductStateClass(shopId: string): string {
  const state = shopProductSyncStatus(shopId)?.state
  if (state === 'failed') return 'text-red-600 dark:text-red-400'
  if (state === 'queued') return 'text-amber-600 dark:text-amber-400'
  if (state === 'refreshing') return 'text-primary-600 dark:text-primary-300'
	const snapshotState = shopProductSyncStatus(shopId)?.snapshot_state
	if (snapshotState === 'expired') return 'text-red-600 dark:text-red-400'
	if (snapshotState === 'stale' || snapshotState === 'legacy') return 'text-amber-600 dark:text-amber-400'
  return 'text-gray-500 dark:text-dark-400'
}

function shopProductRefreshLabel(shopId: string): string {
  if (shopProductRefreshRequests.value[shopId]) return t('publicAccountImport.shopProductsRequesting')
  const state = shopProductSyncStatus(shopId)?.state
  if (state === 'queued') return t('publicAccountImport.shopProductsQueued')
  if (state === 'refreshing') return t('publicAccountImport.shopProductsRefreshing')
  const retryAfter = shopProductRetryAfter(shopId)
  if (retryAfter > 0) return t('publicAccountImport.shopProductsCooldown', { seconds: retryAfter })
  return state === 'failed'
    ? t('publicAccountImport.shopProductsRetry')
    : t('publicAccountImport.shopProductsRefresh')
}

function productPriceStatus(productId: string): ProductPriceStatus | 'idle' {
  return productPriceVerifications.value[productId]?.status || 'idle'
}

function recentProductVerification(productId: string, status?: ProductPriceStatus): ProductPriceVerification | null {
  const verification = productPriceVerifications.value[productId]
  if (!verification || (status && verification.status !== status)) return null
  const maxAge = verification.status === 'failed'
    ? PRODUCT_PRICE_FAILURE_RETRY_MS
    : verification.status === 'unavailable'
      ? PRODUCT_UNAVAILABLE_TTL_MS
      : PRODUCT_PRICE_VERIFICATION_TTL_MS
  return Date.now() - verification.checkedAt <= maxAge ? verification : null
}

function setProductPriceVerification(productId: string, verification: ProductPriceVerification) {
  productPriceVerifications.value = {
    ...productPriceVerifications.value,
    [productId]: verification,
  }
}

function markPublicProductUnavailable(productId: string) {
  setProductPriceVerification(productId, { status: 'unavailable', checkedAt: Date.now() })
  products.value = products.value.filter((item) => item.id !== productId)
}

async function verifyPublicProduct(product: PublicAccountImportProduct, force = false): Promise<boolean> {
  const inProgress = productVerificationPromises.get(product.id)
  if (inProgress) return inProgress

  const recent = recentProductVerification(product.id)
  if (!force && recent) return recent.status === 'verified'

  const task = (async () => {
    setProductPriceVerification(product.id, { status: 'checking', checkedAt: Date.now() })
    try {
      const goodsKey = publicProductGoodsKey(product.url)
      if (!goodsKey) throw new Error('Invalid product URL')
      const response = await postPublicShopAPI('/shopApi/Shop/goodsInfo', {
        goods_key: goodsKey,
        trade_no: null,
      })
			const availability = livePublicProductAvailability(response)
      if (availability === 'unavailable') {
        markPublicProductUnavailable(product.id)
        return false
      }
      if (availability !== 'available') throw new Error(response?.msg || 'Invalid product response')

			const minimumQuantity = livePublicProductMinimumQuantity(response.data)
			if (!minimumQuantity) throw new Error('Invalid product details')

      const shopToken = String(response.data?.user?.token || '').trim()
      if (!shopToken) throw new Error('Invalid product shop')
      const channelResponse = await postPublicShopAPI('/shopApi/Shop/getUserChannel', {
        token: shopToken,
      })
      if (channelResponse?.code !== 1 || !Array.isArray(channelResponse.data)) {
        throw new Error(channelResponse?.msg || 'Invalid payment channels')
      }
			const channel = selectLivePublicProductPaymentChannel(channelResponse.data)
			if (!channel) throw new Error('No payment channel is available')
      const quoteResponse = await postPublicShopAPI('/shopApi/Shop/getGoodsPrice', {
        goods_key: goodsKey,
				quantity: minimumQuantity,
        coupon_code: '',
				channel_id: channel.id,
      })
			const quoteAvailability = livePublicProductQuoteAvailability(quoteResponse)
      if (quoteAvailability === 'unavailable') {
        markPublicProductUnavailable(product.id)
        return false
      }
      if (quoteAvailability !== 'available') {
        throw new Error(quoteResponse?.msg || 'Invalid product quote')
      }

      const verification: ProductPriceVerification = {
        status: 'verified',
        checkedAt: Date.now(),
      }
      setProductPriceVerification(product.id, verification)
      return true
    } catch {
      setProductPriceVerification(product.id, { status: 'failed', checkedAt: Date.now() })
      return false
    } finally {
      productVerificationPromises.delete(product.id)
    }
  })()
  productVerificationPromises.set(product.id, task)
  return task
}

async function handleProductClick(event: MouseEvent, product: PublicAccountImportProduct) {
  event.preventDefault()
  productVerificationMessage.value = ''
  const popup = window.open('about:blank', '_blank')
  if (popup) popup.opener = null
  const verified = await verifyPublicProduct(product, true)
	if (verified) {
		const destination = shopHref(product.url)
    productVerificationMessage.value = ''
    if (popup) popup.location.replace(destination)
    else window.location.assign(destination)
    return
  }

	const unavailable = productPriceVerifications.value[product.id]?.status === 'unavailable'
  productVerificationMessage.value = unavailable
    ? t('publicAccountImport.productUnavailable')
    : t('publicAccountImport.productVerificationFailed')
	popup?.close()
}

async function postPublicShopAPI(path: string, payload: Record<string, unknown>): Promise<any> {
  const response = await fetch(`https://pay.ldxp.cn${path}`, {
    method: 'POST',
    mode: 'cors',
    credentials: 'omit',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      Visitorid: publicProductVisitorID(),
    },
    body: JSON.stringify(payload),
  })
  if (!response.ok) throw new Error(`Shop API returned HTTP ${response.status}`)
  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) throw new Error('Shop API returned a verification page')
  return response.json()
}

function publicProductVisitorID(): string {
  const key = 'sub2api-public-product-visitor'
  try {
    const existing = window.localStorage.getItem(key)
    if (existing) return existing
    const value = createIdempotencyKey().replace(/[^a-zA-Z0-9]/g, '').slice(0, 32)
    window.localStorage.setItem(key, value)
    return value
  } catch {
    return 'sub2apipubliccatalog'
  }
}

function clearShopMessages() {
  shopErrorMessage.value = ''
  shopNoticeMessage.value = ''
}

function shopHref(value: string): string {
  return sanitizeUrl(value)
}

async function handleShopProductRefresh(shop: PublicAccountImportShop) {
  if (!supportsPublicShopProductSync(shop.url) || shopProductRefreshIsDisabled(shop.id)) return
  shopProductRefreshRequests.value = { ...shopProductRefreshRequests.value, [shop.id]: true }
  clearShopMessages()
  try {
    const status = await requestPublicAccountImportProductRefresh(shop.id)
    mergeShopProductSyncStatuses([status], false)
    if (status.accepted) {
      shopNoticeMessage.value = t('publicAccountImport.shopProductsRefreshQueuedNotice', { name: shop.name })
    } else if (status.retry_after_seconds > 0) {
      shopNoticeMessage.value = t('publicAccountImport.shopProductsCooldownNotice', {
        name: shop.name,
        seconds: status.retry_after_seconds,
      })
    } else {
      shopNoticeMessage.value = t('publicAccountImport.shopProductsAlreadyQueuedNotice', { name: shop.name })
    }
    void loadPublicProducts(false)
  } catch (error: any) {
    shopErrorMessage.value = error?.message || t('publicAccountImport.shopProductsRefreshFailed', { name: shop.name })
  } finally {
    const next = { ...shopProductRefreshRequests.value }
    delete next[shop.id]
    shopProductRefreshRequests.value = next
  }
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
    void loadPublicProducts(false)
  } catch (error: any) {
    shopErrorMessage.value = error?.message || t('publicAccountImport.shopSubmitFailed')
  } finally {
    submittingShop.value = false
  }
}
</script>
