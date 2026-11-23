<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorText } from '@patato/api'
import { Alert, Button, Card, Field } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import PageHeader from '../components/PageHeader.vue'
import { useSubscription } from '../session'

const { t } = useI18n()
const { serviceName } = useSubscription()
const current = ref('')
const next = ref('')
const confirm = ref('')
const submitted = ref(false)
const error = ref('')
const done = ref(false)
const busy = ref(false)
const shortError = computed(() => (submitted.value && next.value.length < 8 ? t('newPassword.tooShort') : ''))
const mismatch = computed(() => (confirm.value && confirm.value !== next.value ? t('newPassword.mismatch') : ''))

async function submit() {
  submitted.value = true
  if (next.value.length < 8 || next.value !== confirm.value) return
  error.value = ''
  busy.value = true
  try {
    await api.put('/api/v1/me/password', { body: { current_password: current.value, new_password: next.value } })
    done.value = true
    current.value = next.value = confirm.value = ''
    submitted.value = false
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppShell :service-name="serviceName">
    <PageHeader :title="t('changePassword.title')" :lead="t('changePassword.lead')" />
    <Card as="section" class="flex flex-col gap-4 p-5 md:p-6">
      <Alert v-if="done" tone="ok">{{ t('changePassword.done') }}</Alert>
      <form class="flex flex-col gap-4" novalidate @submit.prevent="submit">
        <Field v-model="current" :label="t('changePassword.current')" type="password" autocomplete="current-password" />
        <Field v-model="next" :label="t('newPassword.password')" type="password" autocomplete="new-password" :hint="t('newPassword.hint')" :error="shortError" />
        <Field v-model="confirm" :label="t('newPassword.confirm')" type="password" autocomplete="new-password" :error="mismatch" />
        <Alert v-if="error">{{ error }}</Alert>
        <Button type="submit" variant="primary" class="min-h-11 self-start max-sm:w-full" :loading="busy" :disabled="!current || !next">{{ t('changePassword.submit') }}</Button>
      </form>
    </Card>
  </AppShell>
</template>
