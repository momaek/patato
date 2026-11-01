<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { api, errorText, fmt, type List, type MyOrder } from '@patato/api'
import { Badge, Card, Empty, LoadState, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const { serviceName } = useSubscription()
const orders = useAuthed(['me', 'orders'], () => api.get<List<MyOrder>>('/api/v1/me/orders'))
const tone: Record<string, Tone> = { pending: 'idle', paid: 'ok', refunded: 'neutral', cancelled: 'neutral' }
</script>

<template>
  <AppShell :service-name="serviceName">
    <h1 class="text-2xl font-semibold">{{ t('orders.title') }}</h1>
    <LoadState :loading="orders.isPending.value" :error="orders.error.value" :message="orders.error.value ? errorText(orders.error.value) : undefined" @retry="orders.refetch()">
      <Card as="ul" class="divide-y divide-line-soft">
        <li v-for="o in orders.data.value?.items ?? []" :key="o.trade_no">
          <RouterLink :to="{ name: 'order', params: { no: o.trade_no } }" class="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 text-ink no-underline hover:bg-bg">
            <span class="min-w-0 flex-[1_1_200px]">
              <span class="font-medium">{{ o.plan_name }} {{ o.period_name }}</span>
              <span class="block text-[13px] text-muted tabular">{{ fmt.dateTime(o.created_at) }}</span>
            </span>
            <span class="font-medium tabular">{{ fmt.money(o.total) }}</span>
            <Badge :tone="tone[o.status]">{{ t(`orders.status.${o.status}`) }}</Badge>
          </RouterLink>
        </li>
        <li v-if="(orders.data.value?.items ?? []).length === 0"><Empty :title="t('orders.none')" /></li>
      </Card>
    </LoadState>
  </AppShell>
</template>
