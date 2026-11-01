<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText, fmt, type MyOrder } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
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
    <RouterLink :to="{ name: 'orders' }" class="-mb-3 font-medium">← {{ t('orders.back') }}</RouterLink>
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <Card v-if="o" as="section" aria-labelledby="order-h" class="flex flex-col gap-4 p-5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h1 id="order-h" class="text-xl font-semibold">{{ o.plan_name }} {{ o.period_name }}</h1>
          <Badge :tone="tone[o.status]">{{ t(`orders.status.${o.status}`) }}</Badge>
        </div>
        <Alert v-if="o.status === 'pending'" tone="accent" role="status">{{ t('orders.waiting') }} {{ t('orders.expiresIn', { time: fmt.until(o.expires_at) }) }}</Alert>
        <Alert v-if="o.status === 'paid'" tone="ok" role="status">{{ o.days ? t('orders.paidNote', { days: o.days }) : t('orders.paidNoteNoDays') }}</Alert>
        <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
          <dt class="text-muted">{{ t('orders.tradeNo') }}</dt><dd class="font-mono text-[13px] break-all">{{ o.trade_no }}</dd>
          <dt class="text-muted">{{ t('buy.amount') }}</dt><dd class="tabular">{{ fmt.money(o.amount) }}</dd>
          <template v-if="o.discount"><dt class="text-muted">{{ t('buy.discount') }}</dt><dd class="tabular">-{{ fmt.money(o.discount) }}</dd></template>
          <template v-if="o.balance_used"><dt class="text-muted">{{ t('buy.balance') }}</dt><dd class="tabular">-{{ fmt.money(o.balance_used) }}</dd></template>
          <dt class="text-muted">{{ t('buy.total') }}</dt><dd class="font-semibold tabular">{{ fmt.money(o.total) }}</dd>
          <dt class="text-muted">{{ t('orders.created') }}</dt><dd class="tabular">{{ fmt.dateTime(o.created_at) }}</dd>
          <template v-if="o.paid_at"><dt class="text-muted">{{ t('orders.paidAt') }}</dt><dd class="tabular">{{ fmt.dateTime(o.paid_at) }}</dd></template>
          <template v-if="o.status === 'refunded'"><dt class="text-muted">{{ t('orders.status.refunded') }}</dt><dd class="tabular">{{ t('orders.refunded', { amount: fmt.money(o.refund_amount) }) }}</dd></template>
        </dl>
        <Alert v-if="error">{{ error }}</Alert>
        <div v-if="o.status === 'pending'" class="flex flex-wrap gap-2">
          <Button variant="primary" :loading="busy === 'pay'" :disabled="!!busy" @click="pay">{{ t('orders.pay') }}</Button>
          <Button :loading="busy === 'cancel'" :disabled="!!busy" @click="cancel">{{ t('orders.cancel') }}</Button>
        </div>
        <RouterLink v-if="o.status === 'paid'" :to="{ name: 'home' }" class="font-medium">{{ t('resetLink.backHome') }}</RouterLink>
      </Card>
    </LoadState>
  </AppShell>
</template>
