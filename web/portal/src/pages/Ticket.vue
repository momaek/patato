<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText, fmt, type Ticket, type TicketMessage } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState, type Tone } from '@patato/ui'
import { Headset, SendHorizontal, UserRound } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
import { useAuthed, useSubscription } from '../session'

type Detail = { ticket: Ticket; messages: TicketMessage[] }
const { t } = useI18n()
const route = useRoute()
const qc = useQueryClient()
const { serviceName } = useSubscription()
const id = computed(() => String(route.params.id))
const q = useAuthed(['me', 'ticket', id], () => api.get<Detail>('/api/v1/me/tickets/{id}', { params: { id: id.value } }), { refetchInterval: 30_000 })
const tone: Record<string, Tone> = { open: 'warn', replied: 'ok', closed: 'neutral' }
const body = ref('')
const busy = ref('')
const error = ref('')

async function act(kind: 'send' | 'close') {
  error.value = ''
  busy.value = kind
  try {
    const d =
      kind === 'send'
        ? await api.post<Detail>('/api/v1/me/tickets/{id}/messages', { params: { id: id.value }, body: { body: body.value.trim() } })
        : await api.post<Detail>('/api/v1/me/tickets/{id}:close', { params: { id: id.value } })
    qc.setQueryData(['me', 'ticket', id], d)
    qc.invalidateQueries({ queryKey: ['me', 'tickets'] })
    if (kind === 'send') body.value = ''
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
      <template v-if="q.data.value">
        <PageHeader :title="q.data.value.ticket.subject" :back="{ name: 'tickets' }" :back-label="t('tickets.back')">
          <template #action><Badge :tone="tone[q.data.value.ticket.status]" class="px-2.5 py-1 text-[13px]">{{ t(`tickets.status.${q.data.value.ticket.status}`) }}</Badge></template>
        </PageHeader>
        <ol class="flex flex-col gap-4">
          <li v-for="m in q.data.value.messages" :key="m.id" :class="['flex gap-2.5', m.author === 'user' ? 'flex-row-reverse' : '']">
            <span
              aria-hidden="true"
              :class="['inline-flex size-9 shrink-0 items-center justify-center rounded-full', m.author === 'user' ? 'bg-accent text-on-accent' : 'bg-card text-accent ring-1 ring-line']"
            >
              <component :is="m.author === 'user' ? UserRound : Headset" class="size-4" />
            </span>
            <div :class="['flex max-w-[82%] flex-col gap-1', m.author === 'user' ? 'items-end' : '']">
              <p class="px-1 text-[12px] text-muted">{{ m.author === 'user' ? t('tickets.you') : t('tickets.support') }} · {{ fmt.dateTime(m.created_at) }}</p>
              <div
                :class="[
                  'rounded-2xl px-4 py-3 whitespace-pre-wrap',
                  m.author === 'user' ? 'rounded-tr-md bg-accent text-on-accent' : 'rounded-tl-md border border-line bg-card shadow-card',
                ]"
              >{{ m.body }}</div>
            </div>
          </li>
        </ol>
        <Alert v-if="q.data.value.ticket.status === 'closed'" tone="accent">{{ t('tickets.closed') }}</Alert>
        <Card as="form" class="flex flex-col gap-3 p-4" @submit.prevent="act('send')">
          <label class="sr-only" for="reply">{{ t('tickets.reply') }}</label>
          <textarea id="reply" v-model="body" rows="4" maxlength="5000" class="field-input py-2.5" :placeholder="t('tickets.reply')" />
          <Alert v-if="error">{{ error }}</Alert>
          <div class="flex items-center justify-between gap-2">
            <Button v-if="q.data.value.ticket.status !== 'closed'" variant="ghost" class="min-h-11" :loading="busy === 'close'" @click="act('close')">{{ t('tickets.close') }}</Button>
            <span v-else />
            <Button type="submit" variant="primary" class="min-h-11" :loading="busy === 'send'" :disabled="!body.trim()">
              <SendHorizontal v-if="busy !== 'send'" class="size-4" aria-hidden="true" />{{ t('tickets.send') }}
            </Button>
          </div>
        </Card>
      </template>
    </LoadState>
  </AppShell>
</template>
