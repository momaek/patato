<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { api, errorText, fmt, type List, type MyOrder } from '@patato/api'
import { Badge, Card, Empty, LoadState, type Tone } from '@patato/ui'
import { ChevronRight, ReceiptText } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
import { useAuthed, usePortalConfig, useSubscription } from '../session'

const { t } = useI18n()
const { serviceName } = useSubscription()
const config = usePortalConfig()
const orders = useAuthed(['me', 'orders'], () => api.get<List<MyOrder>>('/api/v1/me/orders'))
const tone: Record<string, Tone> = { pending: 'idle', paid: 'ok', refunded: 'neutral', cancelled: 'neutral' }
</script>

<template>
  <AppShell :service-name="serviceName">
    <PageHeader :title="t('orders.title')" />
    <LoadState :loading="orders.isPending.value" :error="orders.error.value" :message="orders.error.value ? errorText(orders.error.value) : undefined" @retry="orders.refetch()">
      <Card as="ul" class="overflow-hidden">
        <li v-for="o in orders.data.value?.items ?? []" :key="o.trade_no" class="border-b border-line-soft last:border-b-0">
          <RouterLink :to="{ name: 'order', params: { no: o.trade_no } }" class="group flex items-center gap-3.5 px-4 py-3.5 text-ink no-underline transition-colors duration-150 hover:bg-bg hover:text-ink md:px-5">
            <span class="icon-tile"><ReceiptText class="size-5" aria-hidden="true" /></span>
            <span class="min-w-0 flex-1">
              <span class="block truncate font-semibold">{{ o.plan_name }} {{ o.period_name }}</span>
              <span class="block text-[13px] text-muted tabular">{{ fmt.dateTime(o.created_at) }}</span>
            </span>
            <span class="flex flex-col items-end gap-1">
              <span class="font-semibold tabular">{{ fmt.money(o.total) }}</span>
              <Badge :tone="tone[o.status]">{{ t(`orders.status.${o.status}`) }}</Badge>
            </span>
            <ChevronRight class="size-4 shrink-0 text-muted group-hover:text-accent" aria-hidden="true" />
          </RouterLink>
        </li>
        <li v-if="(orders.data.value?.items ?? []).length === 0">
          <Empty :title="t('orders.none')">
            <RouterLink v-if="config.data.value?.payment" :to="{ name: 'buy' }" class="font-medium">{{ t('orders.goBuy') }}</RouterLink>
          </Empty>
        </li>
      </Card>
    </LoadState>
  </AppShell>
</template>
