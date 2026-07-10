<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="rounded-2xl bg-gradient-to-br from-primary-600 to-emerald-600 p-6 text-white shadow-lg">
        <div class="max-w-3xl space-y-3">
          <p class="text-sm font-semibold uppercase tracking-wide text-white/80">分销返利</p>
          <h1 class="text-2xl font-bold md:text-3xl">购买订阅兑换码，分享给朋友使用</h1>
          <p class="text-sm leading-6 text-white/90 md:text-base">兑换后会开通或续费对应订阅，你按实际支付金额获得分销返利，并绑定邀请关系，之后长期有效。适合社群、客户、团队成员分发，兑换码永久有效，未使用前可复制转发。栖地封站关闭注册后，将关闭此页面，如有未核销兑换码，请找客服处理。</p>
        </div>
      </div>

      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <StatCard label="默认分成比例" :value="`${formatNumber(summary?.default_rebate_rate_percent || 0)}%`" />
        <StatCard label="已购兑换码" :value="String(summary?.total_codes || 0)" />
        <StatCard label="未使用" :value="String(summary?.unused_codes || 0)" />
        <StatCard label="累计分销额" :value="formatCurrency(summary?.total_purchase_pay_amount || 0)" />
        <StatCard label="累计分销返利" :value="formatCurrency(summary?.total_rebate_amount || 0)" highlight />
      </div>

      <div v-if="checkoutLoading" class="flex items-center justify-center py-20">
        <div class="h-8 w-8 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
      </div>
      <template v-else>
        <template v-if="paymentPhase === 'paying'">
          <PaymentStatusPanel
            :order-id="paymentState.orderId"
            :qr-code="paymentState.qrCode"
            :expires-at="paymentState.expiresAt"
            :payment-type="paymentState.paymentType"
            :pay-url="paymentState.payUrl"
            order-type="package_redeem"
            :currency="paymentState.currency || selectedCurrency"
            @done="onPaymentDone"
            @success="onPaymentSuccess"
            @settled="onPaymentSettled"
          />
        </template>
        <template v-else>
          <template v-if="selectedPlan">
            <div class="card p-5">
              <div class="mb-3 flex flex-wrap items-center gap-2">
                <span :class="['rounded-md border px-2 py-0.5 text-xs font-medium', planBadgeClass]">
                  {{ platformLabel(selectedPlan.group_platform || '') }}
                </span>
                <h3 class="text-lg font-bold text-gray-900 dark:text-white">{{ selectedPlan.name }}</h3>
              </div>
              <div class="flex items-baseline gap-2">
                <span v-if="selectedPlan.original_price" class="text-sm text-gray-400 line-through dark:text-gray-500">
                  {{ formatSelectedPaymentAmount(selectedPlan.original_price) }}
                </span>
                <span :class="['text-3xl font-bold', planTextClass]">{{ formatSelectedPaymentAmount(selectedPlan.price) }}</span>
                <span class="text-sm text-gray-500 dark:text-gray-400">/ {{ planValiditySuffix }}</span>
              </div>
              <p v-if="selectedPlan.description" class="mt-2 text-sm leading-relaxed text-gray-500 dark:text-gray-400">
                {{ selectedPlan.description }}
              </p>
              <div class="mt-3 grid grid-cols-2 gap-3">
                <div>
                  <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('payment.planCard.rate') }}</span>
                  <div class="flex items-baseline">
                    <span :class="['text-lg font-bold', planTextClass]">×{{ selectedPlan.rate_multiplier ?? 1 }}</span>
                  </div>
                </div>
                <div v-if="selectedPlan.daily_limit_usd != null">
                  <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('payment.planCard.dailyLimit') }}</span>
                  <div class="text-lg font-semibold text-gray-800 dark:text-gray-200">${{ selectedPlan.daily_limit_usd }}</div>
                </div>
                <div v-if="selectedPlan.weekly_limit_usd != null">
                  <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('payment.planCard.weeklyLimit') }}</span>
                  <div class="text-lg font-semibold text-gray-800 dark:text-gray-200">${{ selectedPlan.weekly_limit_usd }}</div>
                </div>
                <div v-if="selectedPlan.monthly_limit_usd != null">
                  <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('payment.planCard.monthlyLimit') }}</span>
                  <div class="text-lg font-semibold text-gray-800 dark:text-gray-200">${{ selectedPlan.monthly_limit_usd }}</div>
                </div>
                <div v-if="selectedPlan.daily_limit_usd == null && selectedPlan.weekly_limit_usd == null && selectedPlan.monthly_limit_usd == null">
                  <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('payment.planCard.quota') }}</span>
                  <div class="text-lg font-semibold text-gray-800 dark:text-gray-200">{{ t('payment.planCard.unlimited') }}</div>
                </div>
              </div>
            </div>
            <div v-if="enabledMethods.length >= 1" class="card p-6">
              <PaymentMethodSelector
                :methods="subMethodOptions"
                :selected="selectedMethod"
                @select="selectedMethod = $event"
              />
            </div>
            <div v-if="feeRate > 0 && selectedPlan.price > 0" class="card p-6">
              <div class="space-y-2 text-sm">
                <div class="flex justify-between">
                  <span class="text-gray-500 dark:text-gray-400">{{ t('payment.amountLabel') }}</span>
                  <span class="text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(selectedPlan.price) }}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-gray-500 dark:text-gray-400">{{ t('payment.fee') }} ({{ feeRate }}%)</span>
                  <span class="text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(subFeeAmount) }}</span>
                </div>
                <div class="flex justify-between border-t border-gray-200 pt-2 dark:border-dark-600">
                  <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.actualPay') }}</span>
                  <span class="text-lg font-bold text-primary-600 dark:text-primary-400">{{ formatSelectedPaymentAmount(subTotalAmount) }}</span>
                </div>
              </div>
            </div>
            <button :class="['btn w-full py-3 text-base font-medium', paymentButtonClass]" :disabled="!canSubmitSubscription || submitting" @click="confirmSubscribeCode">
              <span v-if="submitting" class="flex items-center justify-center gap-2">
                <span class="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent"></span>
                {{ t('common.processing') }}
              </span>
              <span v-else>购买 {{ formatSelectedPaymentAmount(feeRate > 0 ? subTotalAmount : selectedPlan.price) }}</span>
            </button>
            <button class="btn btn-secondary w-full" @click="selectedPlan = null">{{ t('common.cancel') }}</button>
          </template>
          <template v-else>
            <div v-if="checkout.plans.length === 0" class="card py-16 text-center">
              <Icon name="gift" size="xl" class="mx-auto mb-3 text-gray-300 dark:text-dark-600" />
              <p class="text-gray-500 dark:text-gray-400">{{ t('payment.noPlans') }}</p>
            </div>
            <div v-else :class="planGridClass">
              <SubscriptionPlanCard v-for="plan in checkout.plans" :key="plan.id" :plan="plan" action-label="购买" @select="selectPlan" />
            </div>
          </template>
        </template>
      </template>

      <div class="card p-6">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">我的分销兑换码</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">复制未使用的订阅兑换码发给客户或朋友，对方兑换后你获得分销返利。</p>
          </div>
          <select v-model="codeStatus" class="input w-full sm:w-40" @change="loadCodes(1)">
            <option value="">全部状态</option>
            <option value="unused">未使用</option>
            <option value="used">已使用</option>
          </select>
        </div>
        <div class="mt-5 overflow-x-auto">
          <table class="w-full min-w-[760px] text-left text-sm">
            <thead><tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-dark-400"><th class="px-3 py-2">兑换码</th><th class="px-3 py-2">状态</th><th class="px-3 py-2">订阅天数</th><th class="px-3 py-2">实际支付</th><th class="px-3 py-2">使用时间</th><th class="px-3 py-2 text-right">操作</th></tr></thead>
            <tbody>
              <tr v-for="code in codes" :key="code.id" class="border-b border-gray-100 last:border-b-0 dark:border-dark-800">
                <td class="px-3 py-3 font-mono text-gray-900 dark:text-white">{{ code.code }}</td>
                <td class="px-3 py-3"><span :class="['rounded-full px-2 py-1 text-xs font-medium', code.status === 'unused' ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300']">{{ code.status === 'unused' ? '未使用' : '已使用' }}</span></td>
                <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ code.validity_days || '-' }} 天</td>
                <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ formatCurrency(code.purchase_pay_amount || 0) }}</td>
                <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ code.used_at ? formatDateTime(code.used_at) : '-' }}</td>
                <td class="px-3 py-3 text-right"><button class="btn btn-secondary btn-sm" @click="copyCode(code.code)">复制</button></td>
              </tr>
            </tbody>
          </table>
          <div v-if="!codesLoading && codes.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">暂无兑换码</div>
        </div>
        <div class="mt-4 flex items-center justify-between text-sm text-gray-500"><span>共 {{ pagination.total }} 条</span><div class="flex gap-2"><button class="btn btn-secondary btn-sm" :disabled="pagination.page <= 1" @click="loadCodes(pagination.page - 1)">上一页</button><button class="btn btn-secondary btn-sm" :disabled="pagination.page * pagination.page_size >= pagination.total" @click="loadCodes(pagination.page + 1)">下一页</button></div></div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import SubscriptionPlanCard from '@/components/payment/SubscriptionPlanCard.vue'
import PaymentMethodSelector from '@/components/payment/PaymentMethodSelector.vue'
import PaymentStatusPanel from '@/components/payment/PaymentStatusPanel.vue'
import { paymentAPI } from '@/api/payment'
import packageRedeemAffiliateAPI, { type PackageRedeemAffiliateSummary } from '@/api/packageRedeemAffiliate'
import type { RedeemCode } from '@/types'
import type { CheckoutInfoResponse, CreateOrderResult, SubscriptionPlan } from '@/types/payment'
import { usePaymentStore } from '@/stores/payment'
import { useAppStore } from '@/stores/app'
import { buildCreateOrderPayload, decidePaymentLaunch, getVisibleMethods, normalizeVisibleMethod, writePaymentRecoverySnapshot, type PaymentRecoverySnapshot } from '@/components/payment/paymentFlow'
import type { PaymentMethodOption } from '@/components/payment/PaymentMethodSelector.vue'
import { formatPaymentAmount, normalizePaymentCurrency } from '@/components/payment/currency'
import { platformBadgeClass, platformLabel, platformTextClass } from '@/utils/platformColors'
import { formatCurrency, formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useClipboard } from '@/composables/useClipboard'
import { getPaymentPopupFeatures } from '@/components/payment/providerConfig'

const i18n = useI18n()
const { t } = i18n
const router = useRouter()
const paymentStore = usePaymentStore()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const summary = ref<PackageRedeemAffiliateSummary | null>(null)
const checkout = reactive<CheckoutInfoResponse>({ methods: {}, global_min: 0, global_max: 0, plans: [], balance_disabled: false, balance_recharge_multiplier: 1, subscription_usd_to_cny_rate: 0, recharge_fee_rate: 0, help_text: '', help_image_url: '', stripe_publishable_key: '' })
const checkoutLoading = ref(false)
const submitting = ref(false)
const selectedPlan = ref<SubscriptionPlan | null>(null)
const selectedMethod = ref('')
const codes = ref<RedeemCode[]>([])
const codesLoading = ref(false)
const codeStatus = ref('')
const pagination = reactive({ page: 1, page_size: 10, total: 0 })
const paymentPhase = ref<'select' | 'paying'>('select')

function emptyPaymentState(): PaymentRecoverySnapshot {
  return {
    orderId: 0, amount: 0, qrCode: '', expiresAt: '', paymentType: '', payUrl: '',
    outTradeNo: '', clientSecret: '', intentId: '', currency: '', countryCode: '',
    paymentEnv: '', payAmount: 0, orderType: '', paymentMode: '', resumeToken: '', createdAt: 0,
  }
}

const paymentState = ref<PaymentRecoverySnapshot>(emptyPaymentState())

function resetPayment() {
  paymentPhase.value = 'select'
  paymentState.value = emptyPaymentState()
}

function onPaymentDone() {
  resetPayment()
  selectedPlan.value = null
  loadCodes(1)
  loadSummary()
}

function onPaymentSuccess() {
  loadCodes(1)
  loadSummary()
}

function onPaymentSettled() {}

const visibleMethods = computed(() => getVisibleMethods(checkout.methods))
const enabledMethods = computed(() => Object.keys(visibleMethods.value))
const feeRate = computed(() => checkout.recharge_fee_rate ?? 0)
const selectedLimit = computed(() => visibleMethods.value[selectedMethod.value])
const selectedCurrency = computed(() => normalizePaymentCurrency(selectedLimit.value?.currency))
const localeCode = computed(() => {
  const raw = i18n.locale as unknown
  if (typeof raw === 'string') return raw
  if (raw && typeof raw === 'object' && 'value' in raw) return String((raw as { value?: string }).value || '')
  return undefined
})
const planGridClass = computed(() => {
  const n = checkout.plans.length
  if (n <= 2) return 'grid grid-cols-1 gap-5 sm:grid-cols-2'
  return 'grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3'
})
const subMethodOptions = computed<PaymentMethodOption[]>(() => {
  const planPrice = selectedPlan.value?.price ?? 0
  return enabledMethods.value.map((type) => {
    const ml = visibleMethods.value[type]
    return { type, fee_rate: ml?.fee_rate ?? 0, available: ml?.available !== false && amountFitsMethod(planPrice, type) }
  })
})
const subFeeAmount = computed(() => {
  const price = selectedPlan.value?.price ?? 0
  if (feeRate.value <= 0 || price <= 0) return 0
  return Math.ceil(((price * feeRate.value) / 100) * 100) / 100
})
const subTotalAmount = computed(() => {
  const price = selectedPlan.value?.price ?? 0
  if (feeRate.value <= 0 || price <= 0) return price
  return Math.round((price + subFeeAmount.value) * 100) / 100
})
const canSubmitSubscription = computed(() => selectedPlan.value !== null && amountFitsMethod(selectedPlan.value.price, selectedMethod.value) && selectedLimit.value?.available !== false)
const paymentButtonClass = computed(() => {
  const m = selectedMethod.value
  if (!m) return 'btn-primary'
  if (m.includes('alipay')) return 'btn-alipay'
  if (m.includes('wxpay')) return 'btn-wxpay'
  if (m === 'stripe') return 'btn-stripe'
  if (m === 'airwallex') return 'btn-airwallex'
  return 'btn-primary'
})
const planBadgeClass = computed(() => platformBadgeClass(selectedPlan.value?.group_platform || ''))
const planTextClass = computed(() => platformTextClass(selectedPlan.value?.group_platform || ''))
const planValiditySuffix = computed(() => {
  if (!selectedPlan.value) return ''
  const u = selectedPlan.value.validity_unit || 'day'
  if (u === 'month') return t('payment.perMonth')
  if (u === 'year') return t('payment.perYear')
  return `${selectedPlan.value.validity_days}${t('payment.days')}`
})

function formatNumber(value: number): string { return Number(value || 0).toFixed(2).replace(/\.00$/, '') }
function isMobileDevice(): boolean { return typeof window !== 'undefined' && /Android|iPhone|iPad|iPod|Mobile/i.test(window.navigator.userAgent) }
function formatSelectedPaymentAmount(value: number): string { return formatPaymentAmount(value, selectedCurrency.value, localeCode.value) }
function amountFitsMethod(amt: number, methodType: string): boolean {
  if (amt <= 0) return true
  const ml = visibleMethods.value[methodType]
  if (!ml) return false
  if (ml.single_min > 0 && amt < ml.single_min) return false
  if (ml.single_max > 0 && amt > ml.single_max) return false
  return true
}
function selectPlan(plan: SubscriptionPlan) { selectedPlan.value = plan }

async function loadSummary() { summary.value = await packageRedeemAffiliateAPI.getSummary() }
async function loadCheckoutInfo() {
  checkoutLoading.value = true
  try { Object.assign(checkout, (await paymentAPI.getCheckoutInfo()).data) } catch (error) { appStore.showError(extractApiErrorMessage(error, '加载订阅失败')) } finally { checkoutLoading.value = false }
}
async function loadCodes(page = pagination.page) {
  codesLoading.value = true
  try {
    const data = await packageRedeemAffiliateAPI.listCodes({ page, page_size: pagination.page_size, status: codeStatus.value })
    codes.value = data.items || []
    pagination.page = data.page
    pagination.page_size = data.page_size
    pagination.total = data.total
  } catch (error) { appStore.showError(extractApiErrorMessage(error, '加载兑换码失败')) } finally { codesLoading.value = false }
}
async function copyCode(code: string) { await copyToClipboard(code, '兑换码已复制') }
async function confirmSubscribeCode() {
  if (!selectedPlan.value || submitting.value) return
  const paymentType = normalizeVisibleMethod(selectedMethod.value) || selectedMethod.value
  if (!paymentType) { appStore.showError('暂无可用支付方式'); return }
  submitting.value = true
  try {
    const result = await paymentStore.createOrder(buildCreateOrderPayload({ amount: selectedPlan.value.price, paymentType, orderType: 'package_redeem', planId: selectedPlan.value.id, origin: window.location.origin, isMobile: isMobileDevice(), isWechatBrowser: /MicroMessenger/i.test(window.navigator.userAgent) })) as CreateOrderResult & { resume_token?: string }
    const visibleMethod = normalizeVisibleMethod(paymentType) || paymentType
    const stripeRouteUrl = result.client_secret && visibleMethod !== 'airwallex' ? router.resolve({ path: '/payment/stripe', query: { order_id: String(result.order_id), client_secret: result.client_secret, resume_token: result.resume_token || undefined } }).href : ''
    const airwallexRouteUrl = result.client_secret && result.intent_id ? router.resolve({ path: '/payment/airwallex', query: { order_id: String(result.order_id), out_trade_no: result.out_trade_no || undefined, resume_token: result.resume_token || undefined } }).href : ''
    const decision = decidePaymentLaunch(result, { visibleMethod, orderType: 'package_redeem', isMobile: isMobileDevice(), isWechatBrowser: /MicroMessenger/i.test(window.navigator.userAgent), stripeRouteUrl, stripePopupUrl: stripeRouteUrl, airwallexRouteUrl })

    paymentState.value = decision.paymentState
    paymentPhase.value = 'paying'
    writePaymentRecoverySnapshot(window.localStorage, decision.recovery)

    if (decision.kind === 'stripe_popup') {
      const win = window.open(decision.paymentState.payUrl, 'paymentPopup', getPaymentPopupFeatures())
      if (!win || win.closed) window.location.href = decision.paymentState.payUrl
      return
    }
    if (decision.kind === 'stripe_route' || decision.kind === 'airwallex_route') {
      window.location.href = decision.paymentState.payUrl
      return
    }
    if (decision.kind === 'redirect_waiting' && decision.paymentState.payUrl) {
      if (isMobileDevice()) {
        window.location.href = decision.paymentState.payUrl
      } else {
        const win = window.open(decision.paymentState.payUrl, 'paymentPopup', getPaymentPopupFeatures())
        if (!win || win.closed) window.location.href = decision.paymentState.payUrl
      }
    }
  } catch (error) { appStore.showError(extractApiErrorMessage(error, '创建订单失败')) } finally { submitting.value = false }
}

watch(enabledMethods, (methods) => {
  if (!selectedMethod.value || !methods.includes(selectedMethod.value)) selectedMethod.value = methods[0] || ''
}, { immediate: true })

onMounted(() => { void Promise.all([loadSummary(), loadCheckoutInfo(), loadCodes(1)]) })
</script>

<script lang="ts">
import { defineComponent, h } from 'vue'
const StatCard = defineComponent({ props: { label: { type: String, required: true }, value: { type: String, required: true }, highlight: Boolean }, setup(props) { return () => h('div', { class: 'card p-5' }, [h('p', { class: 'text-sm text-gray-500 dark:text-dark-400' }, props.label), h('p', { class: ['mt-2 text-2xl font-semibold', props.highlight ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-900 dark:text-white'] }, props.value)]) } })
</script>
