<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, errorText, fmt, type MyOrder, type MyPlans, type Quote } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, LoadState } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const router = useRouter()
const { serviceName } = useSubscription()
const plans = useAuthed(['me', 'plans'], () => api.get<MyPlans>('/api/v1/me/plans'))

const plan = ref('')
const period = ref('')
const coupon = ref('')
const appliedCoupon = ref('')
const useBalance = ref(true)
const method = ref('')
const quote = ref<Quote>()
const quoteError = ref('')
const busy = ref(false)
const error = ref('')

// 默认选当前套餐（续费），否则第一个
watch(
  () => plans.data.value,
  (d) => {
    if (!d || plan.value) return
    const cur = d.items.find((p) => p.current) ?? d.items[0]
    if (cur) {
      plan.value = cur.code
      period.value = cur.prices.find((p) => p.period !== 'reset_traffic')?.period ?? cur.prices[0]?.period ?? ''
    }
    method.value = d.methods[0]?.code ?? ''
  },
  { immediate: true },
)
const selected = computed(() => plans.data.value?.items.find((p) => p.code === plan.value))
// 「起」价不算流量重置包，它不是一个可以单独购买的套餐周期
const fromPrice = (prices: { period: string; price: number }[]) => {
  const own = prices.filter((x) => x.period !== 'reset_traffic')
  return Math.min(...(own.length ? own : prices).map((x) => x.price))
}
function pick(code: string) {
  plan.value = code
  const p = plans.data.value?.items.find((x) => x.code === code)
  if (p && !p.prices.some((x) => x.period === period.value)) period.value = p.prices[0]?.period ?? ''
}

// 价格以服务端试算为准：优惠码、余额抵扣、灰度价格都在服务端算
let seq = 0
async function refresh() {
  if (!plan.value || !period.value) return
  const n = ++seq
  quoteError.value = ''
  try {
    const q = await api.post<Quote>('/api/v1/me/orders:quote', { body: { plan: plan.value, period: period.value, coupon: appliedCoupon.value, use_balance: useBalance.value } })
    if (n === seq) quote.value = q
  } catch (e) {
    if (n !== seq) return
    quote.value = undefined
    quoteError.value = errorText(e)
    if (appliedCoupon.value) appliedCoupon.value = ''
  }
}
watch([plan, period, appliedCoupon, useBalance], refresh)
watch(() => plans.data.value, refresh)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const r = await api.post<{ order: MyOrder; pay_url: string }>('/api/v1/me/orders', {
      body: { plan: plan.value, period: period.value, coupon: appliedCoupon.value, use_balance: useBalance.value, method: method.value },
    })
    if (r.pay_url) window.location.assign(r.pay_url)
    else router.push({ name: 'order', params: { no: r.order.trade_no } })
  } catch (e) {
    error.value = errorText(e)
    busy.value = false
  }
}
</script>

<template>
  <AppShell :service-name="serviceName" wide>
    <div class="flex flex-col gap-1.5">
      <h1 class="text-2xl font-semibold">{{ t('buy.title') }}</h1>
      <p class="text-muted">{{ t('buy.lead') }}</p>
    </div>
    <LoadState :loading="plans.isPending.value" :error="plans.error.value" :message="plans.error.value ? errorText(plans.error.value) : undefined" @retry="plans.refetch()">
      <template v-if="plans.data.value">
        <Alert v-if="plans.data.value.methods.length === 0" tone="warn">{{ t('buy.unavailable') }}</Alert>
        <Empty v-if="plans.data.value.items.length === 0" :title="t('buy.none')" />
        <div v-else class="flex flex-wrap items-start gap-7">
          <div class="flex min-w-0 flex-[3_1_460px] flex-col gap-5">
            <div role="radiogroup" :aria-label="t('buy.title')" class="grid gap-3 sm:grid-cols-2">
              <button
                v-for="p in plans.data.value.items"
                :key="p.code"
                type="button"
                role="radio"
                :aria-checked="plan === p.code"
                :class="['flex flex-col gap-1 rounded-xl border bg-card p-4 text-left', plan === p.code ? 'border-accent ring-2 ring-accent/20' : 'border-line hover:border-accent/60']"
                @click="pick(p.code)"
              >
                <span class="flex items-center justify-between gap-2">
                  <span class="text-lg font-semibold">{{ p.name }}</span>
                  <Badge v-if="p.current" tone="accent">{{ t('buy.current') }}</Badge>
                </span>
                <span class="text-[13px] text-muted">
                  {{ t('buy.transfer', { traffic: fmt.bytes(p.transfer_bytes) }) }}，{{ p.device_limit ? t('buy.devices', { n: p.device_limit }) : t('buy.devicesUnlimited') }}
                </span>
                <span class="font-medium tabular">{{ t('buy.from', { price: fmt.money(fromPrice(p.prices)) }) }}</span>
              </button>
            </div>
            <fieldset v-if="selected" class="flex flex-col gap-2">
              <legend class="mb-1 font-medium">{{ t('buy.period') }}</legend>
              <div class="flex flex-wrap gap-2">
                <label
                  v-for="pr in selected.prices"
                  :key="pr.period"
                  :class="['flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2', period === pr.period ? 'border-accent bg-accent-soft/50' : 'border-line bg-card']"
                >
                  <input v-model="period" type="radio" :value="pr.period" class="accent-accent" />
                  <span>{{ pr.name }}</span>
                  <span class="font-medium tabular">{{ fmt.money(pr.price) }}</span>
                </label>
              </div>
            </fieldset>
          </div>

          <Card as="section" aria-labelledby="pay-h" class="flex min-w-0 flex-[2_1_300px] flex-col gap-4 p-5">
            <h2 id="pay-h" class="text-lg font-semibold">{{ quote ? t(`buy.kind.${quote.kind}`) : t('buy.title') }}</h2>
            <form class="flex gap-2" @submit.prevent="appliedCoupon = coupon.trim()">
              <label class="sr-only" for="coupon">{{ t('buy.coupon') }}</label>
              <input id="coupon" v-model="coupon" :placeholder="t('buy.coupon')" class="field-input min-w-0 flex-1" />
              <Button type="submit" :disabled="!coupon.trim()">{{ t('buy.couponApply') }}</Button>
            </form>
            <label v-if="plans.data.value.balance > 0" class="flex items-center gap-2">
              <input v-model="useBalance" type="checkbox" class="accent-accent" />{{ t('buy.useBalance', { amount: fmt.money(plans.data.value.balance) }) }}
            </label>
            <label v-if="plans.data.value.methods.length > 1" class="flex flex-col gap-1.5">
              <span class="font-medium">{{ t('buy.method') }}</span>
              <select v-model="method" class="field-input"><option v-for="m in plans.data.value.methods" :key="m.code" :value="m.code">{{ m.name }}</option></select>
            </label>
            <Alert v-if="quoteError" tone="warn">{{ quoteError }}</Alert>
            <dl v-if="quote" class="grid grid-cols-[1fr_auto] gap-y-1.5 border-t border-line-soft pt-3">
              <dt class="text-muted">{{ t('buy.amount') }}</dt><dd class="text-right tabular">{{ fmt.money(quote.amount) }}</dd>
              <template v-if="quote.discount"><dt class="text-muted">{{ t('buy.discount') }}</dt><dd class="text-right tabular">-{{ fmt.money(quote.discount) }}</dd></template>
              <template v-if="quote.balance_used"><dt class="text-muted">{{ t('buy.balance') }}</dt><dd class="text-right tabular">-{{ fmt.money(quote.balance_used) }}</dd></template>
              <dt class="text-lg font-semibold">{{ t('buy.total') }}</dt><dd class="text-right text-lg font-semibold tabular">{{ fmt.money(quote.total) }}</dd>
            </dl>
            <ul v-if="quote?.notes.length" class="flex list-disc flex-col gap-1 pl-5 text-[13px] text-muted">
              <li v-for="n in quote.notes" :key="n">{{ n }}</li>
            </ul>
            <Alert v-if="error">{{ error }}</Alert>
            <Button
              variant="primary"
              size="lg"
              block
              :loading="busy"
              :disabled="!quote || (quote.total > 0 && !method)"
              @click="submit"
            >
              {{ quote && quote.total === 0 ? t('buy.submitFree') : t('buy.submit') }}
            </Button>
          </Card>
        </div>
      </template>
    </LoadState>
  </AppShell>
</template>
