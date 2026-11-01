<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, errorText, fmt, type List, type Ticket } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, Field, LoadState, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import { useAuthed, useSubscription } from '../session'

const { t } = useI18n()
const router = useRouter()
const { serviceName } = useSubscription()
const list = useAuthed(['me', 'tickets'], () => api.get<List<Ticket>>('/api/v1/me/tickets'))
const tone: Record<string, Tone> = { open: 'warn', replied: 'ok', closed: 'neutral' }

const creating = ref(false)
const subject = ref('')
const body = ref('')
const busy = ref(false)
const error = ref('')
async function submit() {
  error.value = ''
  busy.value = true
  try {
    const d = await api.post<{ ticket: Ticket }>('/api/v1/me/tickets', { body: { subject: subject.value.trim(), body: body.value.trim() } })
    router.push({ name: 'ticket', params: { id: d.ticket.id } })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <div class="flex flex-wrap items-end justify-between gap-3">
      <div class="flex flex-col gap-1">
        <h1 class="text-2xl font-semibold">{{ t('tickets.title') }}</h1>
        <p class="text-muted">{{ t('tickets.lead') }}</p>
      </div>
      <Button v-if="!creating" variant="primary" @click="creating = true">{{ t('tickets.new') }}</Button>
    </div>
    <Card v-if="creating" as="form" class="flex flex-col gap-4 p-5" @submit.prevent="submit">
      <Field v-model="subject" :label="t('tickets.subject')" required />
      <label class="flex flex-col gap-1.5">
        <span class="font-medium">{{ t('tickets.body') }}</span>
        <textarea v-model="body" rows="6" maxlength="5000" class="field-input py-2" :placeholder="t('tickets.bodyHint')" />
      </label>
      <Alert v-if="error">{{ error }}</Alert>
      <div class="flex justify-end gap-2">
        <Button @click="creating = false">{{ t('resetLink.cancel') }}</Button>
        <Button type="submit" variant="primary" :loading="busy" :disabled="!subject.trim() || !body.trim()">{{ t('tickets.submit') }}</Button>
      </div>
    </Card>
    <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
      <Card as="ul" class="divide-y divide-line-soft">
        <li v-for="x in list.data.value?.items ?? []" :key="x.id">
          <RouterLink :to="{ name: 'ticket', params: { id: x.id } }" class="flex items-center justify-between gap-3 px-4 py-3 text-ink no-underline hover:bg-bg">
            <span class="min-w-0">
              <span class="block truncate font-medium">{{ x.subject }}</span>
              <span class="text-[13px] text-muted">{{ fmt.dateTime(x.last_message_at) }}</span>
            </span>
            <Badge :tone="tone[x.status]">{{ t(`tickets.status.${x.status}`) }}</Badge>
          </RouterLink>
        </li>
        <li v-if="(list.data.value?.items ?? []).length === 0"><Empty :title="t('tickets.none')" /></li>
      </Card>
    </LoadState>
  </AppShell>
</template>
