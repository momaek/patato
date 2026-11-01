<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText, fmt, type Ticket, type TicketMessage } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
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
    <RouterLink :to="{ name: 'tickets' }" class="-mb-3 font-medium">← {{ t('tickets.back') }}</RouterLink>
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <template v-if="q.data.value">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h1 class="text-xl font-semibold">{{ q.data.value.ticket.subject }}</h1>
          <Badge :tone="tone[q.data.value.ticket.status]">{{ t(`tickets.status.${q.data.value.ticket.status}`) }}</Badge>
        </div>
        <ol class="flex flex-col gap-3">
          <li v-for="m in q.data.value.messages" :key="m.id" :class="['flex', m.author === 'user' ? 'justify-end' : '']">
            <Card :class="'flex max-w-[88%] flex-col gap-1 px-4 py-3' + (m.author === 'user' ? ' bg-accent-soft/50' : '')">
              <p class="text-[13px] text-muted">{{ m.author === 'user' ? t('tickets.you') : t('tickets.support') }} · {{ fmt.dateTime(m.created_at) }}</p>
              <p class="whitespace-pre-wrap">{{ m.body }}</p>
            </Card>
          </li>
        </ol>
        <Alert v-if="q.data.value.ticket.status === 'closed'" tone="accent">{{ t('tickets.closed') }}</Alert>
        <form class="flex flex-col gap-3" @submit.prevent="act('send')">
          <label class="sr-only" for="reply">{{ t('tickets.reply') }}</label>
          <textarea id="reply" v-model="body" rows="4" maxlength="5000" class="field-input py-2" :placeholder="t('tickets.reply')" />
          <Alert v-if="error">{{ error }}</Alert>
          <div class="flex justify-between gap-2">
            <Button v-if="q.data.value.ticket.status !== 'closed'" variant="ghost" :loading="busy === 'close'" @click="act('close')">{{ t('tickets.close') }}</Button>
            <span v-else />
            <Button type="submit" variant="primary" :loading="busy === 'send'" :disabled="!body.trim()">{{ t('tickets.send') }}</Button>
          </div>
        </form>
      </template>
    </LoadState>
  </AppShell>
</template>
