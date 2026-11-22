<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText } from '@patato/api'
import { Alert, Button, Card } from '@patato/ui'
import { CircleCheck, TriangleAlert } from 'lucide-vue-next'
import AppShell from '../components/AppShell.vue'
import SubscriptionLink from '../components/SubscriptionLink.vue'
import { useSubscription } from '../session'

const { t } = useI18n()
const qc = useQueryClient()
const { data: sub, serviceName } = useSubscription()
const newURL = ref('')
const error = ref('')
const busy = ref(false)

async function reset() {
  error.value = ''
  busy.value = true
  try {
    const r = await api.post<{ subscription_url: string }>('/api/v1/me/subscription:reset-token')
    newURL.value = r.subscription_url
    await qc.invalidateQueries({ queryKey: ['me', 'subscription'] })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <Card v-if="!newURL" as="section" aria-labelledby="reset-h" class="flex flex-col gap-4 p-5 md:p-6">
      <span class="icon-tile size-12 bg-warn-soft text-warn"><TriangleAlert class="size-6" aria-hidden="true" /></span>
      <h1 id="reset-h" class="text-[22px] font-bold tracking-tight">{{ t('resetLink.title') }}</h1>
      <p>{{ sub && sub.devices_online > 0 ? t('resetLink.lead', { devices: sub.devices_online }) : t('resetLink.leadNoDevices') }}</p>
      <p class="text-[13px] text-muted">{{ t('resetLink.note') }}</p>
      <Alert v-if="error">{{ error }}</Alert>
      <div class="flex flex-wrap gap-2 border-t border-line-soft pt-4">
        <Button variant="danger" class="min-h-11 max-sm:flex-1" :loading="busy" @click="reset">{{ t('resetLink.submit') }}</Button>
        <RouterLink :to="{ name: 'home' }" class="btn-secondary max-sm:flex-1">{{ t('resetLink.cancel') }}</RouterLink>
      </div>
    </Card>
    <Card v-else as="section" class="flex flex-col gap-4 p-5 md:p-6" role="status">
      <span class="icon-tile size-12 bg-ok-soft text-ok"><CircleCheck class="size-6" aria-hidden="true" /></span>
      <h1 class="text-[22px] font-bold tracking-tight">{{ t('resetLink.doneTitle') }}</h1>
      <p class="text-muted">{{ t('resetLink.done') }}</p>
      <SubscriptionLink :url="newURL" />
      <RouterLink :to="{ name: 'home' }" class="inline-flex min-h-11 items-center self-start font-medium">{{ t('resetLink.backHome') }}</RouterLink>
    </Card>
  </AppShell>
</template>
