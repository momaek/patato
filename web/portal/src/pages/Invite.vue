<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, type MyReferral } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, LoadState, type Tone } from '@patato/ui'
import { Copy, Gift } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const { serviceName } = useSubscription()
const q = useAuthed(['me', 'referral'], () => api.get<MyReferral>('/api/v1/me/referral'))
const tone: Record<string, Tone> = { pending: 'idle', frozen: 'warn', settled: 'ok', cancelled: 'neutral' }

const stats = computed(() => {
  const d = q.data.value
  if (!d) return []
  return [
    { label: t('invite.invited'), value: String(d.invited) },
    { label: t('invite.pending'), value: fmt.money(d.pending) },
    { label: t('invite.settled'), value: fmt.money(d.settled) },
    { label: t('invite.balance'), value: fmt.money(d.balance) },
  ]
})

function copy(text: string) {
  navigator.clipboard.writeText(text).then(() => toast.success(t('common.copied')), (e) => toast.error(errorText(e)))
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <template v-if="q.data.value">
        <PageHeader :title="t('invite.title')" :lead="q.data.value.enabled ? t('invite.lead', { rate: q.data.value.rate_percent, days: q.data.value.wait_days }) : undefined" />
        <Alert v-if="!q.data.value.enabled" tone="accent">{{ t('invite.disabled') }}</Alert>
        <Card as="section" aria-labelledby="link-h" class="relative flex flex-col gap-4 overflow-hidden p-5 md:p-6">
          <div aria-hidden="true" class="pointer-events-none absolute -top-16 -right-16 size-48 rounded-full bg-accent/10 blur-2xl" />
          <div class="relative flex items-start gap-3.5">
            <span class="icon-tile"><Gift class="size-5" aria-hidden="true" /></span>
            <div class="flex min-w-0 flex-col gap-0.5">
              <h2 id="link-h" class="font-semibold">{{ t('invite.link') }}</h2>
              <p class="text-[13px] text-muted">{{ t('invite.code') }}：<span class="font-mono font-medium text-ink">{{ q.data.value.invite_code }}</span></p>
            </div>
          </div>
          <template v-if="q.data.value.invite_url">
            <p class="relative rounded-control border border-line-soft bg-bg px-3.5 py-3 font-mono text-[13px] break-all">{{ q.data.value.invite_url }}</p>
            <Button variant="primary" class="relative min-h-11 self-start max-sm:w-full" @click="copy(q.data.value.invite_url)">
              <Copy class="size-4" aria-hidden="true" />{{ t('common.copy') }}
            </Button>
          </template>
          <p v-else class="relative text-muted">{{ t('invite.noLink') }}</p>
        </Card>
        <dl class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Card v-for="s in stats" :key="s.label" class="flex flex-col gap-1 p-4">
            <dt class="text-[13px] text-muted">{{ s.label }}</dt>
            <dd class="text-[22px] font-bold tracking-tight tabular">{{ s.value }}</dd>
          </Card>
        </dl>
        <section aria-labelledby="rec-h" class="flex flex-col gap-3">
          <h2 id="rec-h" class="text-lg font-semibold">{{ t('invite.records') }}</h2>
          <Card as="ul" class="overflow-hidden">
            <li v-for="c in q.data.value.items" :key="c.id" class="flex items-center gap-3.5 border-b border-line-soft px-4 py-3.5 last:border-b-0 md:px-5">
              <span class="min-w-0 flex-1">
                <span class="block truncate font-medium">{{ c.invitee_email }}</span>
                <span class="block text-[13px] text-muted tabular">{{ fmt.dateTime(c.created_at) }}</span>
              </span>
              <span class="flex flex-col items-end gap-1">
                <span class="font-semibold tabular">{{ fmt.money(c.amount) }}</span>
                <Badge :tone="tone[c.status]">{{ t(`invite.state.${c.status}`) }}</Badge>
              </span>
            </li>
            <li v-if="q.data.value.items.length === 0"><Empty :title="t('invite.none')" /></li>
          </Card>
        </section>
      </template>
    </LoadState>
  </AppShell>
</template>
