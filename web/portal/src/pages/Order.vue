<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText, fmt, type MyOrder } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState, type Tone } from '@patato/ui'
import { CircleCheck, Loader2 } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const route = useRoute()
const qc = useQueryClient()
const { serviceName } = useSubscription()
const no = computed(() => String(route.params.no))
// 从支付页跳回来时订单可能还没收到回调：待支付时每 3 秒刷新一次
const q = useAuthed(['me', 'order', no], () => api.get<{ order: MyOrder }>('/api/v1/me/orders/{id}', { params: { id: no.value } }), {
  refetchInterval: (query) => (query.state.data?.order.status === 'pending' ? 3000 : false),
})
const o = computed(() => q.data.value?.order)
const tone: Record<string, Tone> = { pending: 'idle', paid: 'ok', refunded: 'neutral', cancelled: 'neutral' }
const busy = ref('')
const error = ref('')

async function pay() {
  error.value = ''
  busy.value = 'pay'
  try {
    const r = await api.post<{ pay_url: string }>('/api/v1/me/orders/{id}:pay', { params: { id: no.value } })
    if (r.pay_url) window.location.assign(r.pay_url)
  } catch (e) {
    error.value = errorText(e)
    busy.value = ''
  }
}
async function cancel() {
  error.value = ''
  busy.value = 'cancel'
  try {
    await api.post('/api/v1/me/orders/{id}:cancel', { params: { id: no.value } })
    await q.refetch()
    qc.invalidateQueries({ queryKey: ['me'] })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <template v-if="o">
        <PageHeader :title="`${o.plan_name} ${o.period_name}`" :back="{ name: 'orders' }" :back-label="t('orders.back')">
          <template #action><Badge :tone="tone[o.status]" class="px-2.5 py-1 text-[13px]">{{ t(`orders.status.${o.status}`) }}</Badge></template>
        </PageHeader>
        <Card as="section" :aria-label="t('orders.detailAria')" class="flex flex-col gap-5 p-5 md:p-6">
          <Alert v-if="o.status === 'pending'" tone="accent" role="status">
            <span class="inline-flex items-start gap-2"><Loader2 class="mt-0.5 size-4 shrink-0 animate-spin" aria-hidden="true" />{{ t('orders.waiting') }} {{ t('orders.expiresIn', { time: fmt.until(o.expires_at) }) }}</span>
          </Alert>
          <Alert v-if="o.status === 'paid'" tone="ok" role="status">
            <span class="inline-flex items-start gap-2"><CircleCheck class="mt-0.5 size-4 shrink-0" aria-hidden="true" />{{ o.days ? t('orders.paidNote', { days: o.days }) : t('orders.paidNoteNoDays') }}</span>
          </Alert>
          <div class="flex flex-col items-center gap-1 rounded-control bg-bg py-5">
            <span class="text-[13px] text-muted">{{ t('buy.total') }}</span>
            <span class="text-[32px] leading-none font-bold tracking-tight tabular">{{ fmt.money(o.total) }}</span>
          </div>
          <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 text-[14px]">
            <dt class="text-muted">{{ t('orders.tradeNo') }}</dt><dd class="text-right font-mono text-[13px] break-all">{{ o.trade_no }}</dd>
            <dt class="text-muted">{{ t('buy.amount') }}</dt><dd class="text-right tabular">{{ fmt.money(o.amount) }}</dd>
            <template v-if="o.discount"><dt class="text-muted">{{ t('buy.discount') }}</dt><dd class="text-right text-ok tabular">-{{ fmt.money(o.discount) }}</dd></template>
            <template v-if="o.balance_used"><dt class="text-muted">{{ t('buy.balance') }}</dt><dd class="text-right text-ok tabular">-{{ fmt.money(o.balance_used) }}</dd></template>
            <dt class="text-muted">{{ t('orders.created') }}</dt><dd class="text-right tabular">{{ fmt.dateTime(o.created_at) }}</dd>
            <template v-if="o.paid_at"><dt class="text-muted">{{ t('orders.paidAt') }}</dt><dd class="text-right tabular">{{ fmt.dateTime(o.paid_at) }}</dd></template>
            <template v-if="o.status === 'refunded'"><dt class="text-muted">{{ t('orders.status.refunded') }}</dt><dd class="text-right tabular">{{ t('orders.refunded', { amount: fmt.money(o.refund_amount) }) }}</dd></template>
          </dl>
          <Alert v-if="error">{{ error }}</Alert>
          <div v-if="o.status === 'pending'" class="flex flex-wrap gap-2 border-t border-line-soft pt-5">
            <Button variant="primary" class="min-h-11 max-sm:flex-1" :loading="busy === 'pay'" :disabled="!!busy" @click="pay">{{ t('orders.pay') }}</Button>
            <Button class="min-h-11 max-sm:flex-1" :loading="busy === 'cancel'" :disabled="!!busy" @click="cancel">{{ t('orders.cancel') }}</Button>
          </div>
          <RouterLink v-if="o.status === 'paid'" :to="{ name: 'home' }" class="btn-primary self-start max-sm:w-full">{{ t('resetLink.backHome') }}</RouterLink>
        </Card>
      </template>
    </LoadState>
  </AppShell>
</template>
