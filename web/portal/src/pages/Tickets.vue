<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, errorText, fmt, type List, type Ticket } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, Field, LoadState, type Tone } from '@patato/ui'
import { ChevronRight, MessageSquareText, Plus } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
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
    <PageHeader :title="t('tickets.title')" :lead="t('tickets.lead')">
      <template #action>
        <Button v-if="!creating" variant="primary" class="min-h-11" @click="creating = true"><Plus class="size-4" aria-hidden="true" />{{ t('tickets.new') }}</Button>
      </template>
    </PageHeader>
    <Card v-if="creating" as="form" class="flex flex-col gap-4 p-5" @submit.prevent="submit">
      <h2 class="font-semibold">{{ t('tickets.new') }}</h2>
      <Field v-model="subject" :label="t('tickets.subject')" required />
      <label class="flex flex-col gap-1.5">
        <span class="font-medium">{{ t('tickets.body') }}</span>
        <textarea v-model="body" rows="6" maxlength="5000" class="field-input py-2.5" :placeholder="t('tickets.bodyHint')" />
      </label>
      <Alert v-if="error">{{ error }}</Alert>
      <div class="flex justify-end gap-2">
        <Button class="min-h-11" @click="creating = false">{{ t('resetLink.cancel') }}</Button>
        <Button type="submit" variant="primary" class="min-h-11" :loading="busy" :disabled="!subject.trim() || !body.trim()">{{ t('tickets.submit') }}</Button>
      </div>
    </Card>
    <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
      <Card as="ul" class="overflow-hidden">
        <li v-for="x in list.data.value?.items ?? []" :key="x.id" class="border-b border-line-soft last:border-b-0">
          <RouterLink :to="{ name: 'ticket', params: { id: x.id } }" class="group flex items-center gap-3.5 px-4 py-3.5 text-ink no-underline transition-colors duration-150 hover:bg-bg hover:text-ink md:px-5">
            <span class="icon-tile"><MessageSquareText class="size-5" aria-hidden="true" /></span>
            <span class="min-w-0 flex-1">
              <span class="block truncate font-semibold">{{ x.subject }}</span>
              <span class="block text-[13px] text-muted tabular">{{ fmt.dateTime(x.last_message_at) }}</span>
            </span>
            <Badge :tone="tone[x.status]">{{ t(`tickets.status.${x.status}`) }}</Badge>
            <ChevronRight class="size-4 shrink-0 text-muted group-hover:text-accent" aria-hidden="true" />
          </RouterLink>
        </li>
        <li v-if="(list.data.value?.items ?? []).length === 0"><Empty :title="t('tickets.none')">{{ t('tickets.noneHint') }}</Empty></li>
      </Card>
    </LoadState>
  </AppShell>
</template>
