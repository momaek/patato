<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, type MyReferral } from '@patato/api'
import { Alert, Badge, Button, Card, Code, Empty, LoadState, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const { serviceName } = useSubscription()
const q = useAuthed(['me', 'referral'], () => api.get<MyReferral>('/api/v1/me/referral'))
const tone: Record<string, Tone> = { pending: 'idle', frozen: 'warn', settled: 'ok', cancelled: 'neutral' }

function copy(text: string) {
  navigator.clipboard.writeText(text).then(() => toast.success(t('common.copied')), (e) => toast.error(errorText(e)))
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <h1 class="text-2xl font-semibold">{{ t('invite.title') }}</h1>
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <template v-if="q.data.value">
        <p v-if="q.data.value.enabled" class="text-muted">{{ t('invite.lead', { rate: q.data.value.rate_percent, days: q.data.value.wait_days }) }}</p>
        <Alert v-else tone="accent">{{ t('invite.disabled') }}</Alert>
        <Card as="section" class="flex flex-col gap-3 p-5">
          <template v-if="q.data.value.invite_url">
            <h2 class="font-semibold">{{ t('invite.link') }}</h2>
            <Code :text="q.data.value.invite_url" block />
            <Button class="self-start" @click="copy(q.data.value.invite_url)">{{ t('common.copy') }}</Button>
          </template>
          <template v-else>
            <p class="text-muted">{{ t('invite.noLink') }}</p>
          </template>
          <p class="text-[13px] text-muted">{{ t('invite.code') }}：<span class="font-mono text-ink">{{ q.data.value.invite_code }}</span></p>
        </Card>
        <Card as="dl" class="grid grid-cols-2 gap-4 p-5 sm:grid-cols-4">
          <div><dt class="text-muted">{{ t('invite.invited') }}</dt><dd class="text-xl font-semibold tabular">{{ q.data.value.invited }}</dd></div>
          <div><dt class="text-muted">{{ t('invite.pending') }}</dt><dd class="text-xl font-semibold tabular">{{ fmt.money(q.data.value.pending) }}</dd></div>
          <div><dt class="text-muted">{{ t('invite.settled') }}</dt><dd class="text-xl font-semibold tabular">{{ fmt.money(q.data.value.settled) }}</dd></div>
          <div><dt class="text-muted">{{ t('invite.balance') }}</dt><dd class="text-xl font-semibold tabular">{{ fmt.money(q.data.value.balance) }}</dd></div>
        </Card>
        <section aria-labelledby="rec-h" class="flex flex-col gap-3">
          <h2 id="rec-h" class="text-lg font-semibold">{{ t('invite.records') }}</h2>
          <Card as="ul" class="divide-y divide-line-soft">
            <li v-for="c in q.data.value.items" :key="c.id" class="flex items-center justify-between gap-3 px-4 py-3">
              <span>
                <span class="block">{{ c.invitee_email }}</span>
                <span class="text-[13px] text-muted">{{ fmt.dateTime(c.created_at) }}</span>
              </span>
              <span class="flex items-center gap-2">
                <span class="font-medium tabular">{{ fmt.money(c.amount) }}</span>
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
