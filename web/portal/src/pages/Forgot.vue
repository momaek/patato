<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, errorText } from '@patato/api'
import { Alert, Button, Card, Field } from '@patato/ui'
import { MailCheck } from 'lucide-vue-next'
import AuthShell from '../components/AuthShell.vue'
import { supportContact } from '../session'

const { t } = useI18n()
const route = useRoute()
const email = ref(typeof route.query.email === 'string' ? route.query.email : '')
const sentTo = ref('')
const error = ref('')
const busy = ref(false)
const wait = ref(0)
let timer: ReturnType<typeof setInterval> | undefined

async function send() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/v1/me/password:forgot', { body: { email: email.value.trim() } })
    sentTo.value = email.value.trim()
    wait.value = 60
    clearInterval(timer)
    timer = setInterval(() => {
      wait.value = Math.max(0, wait.value - 1)
      if (wait.value === 0) clearInterval(timer)
    }, 1000)
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
onUnmounted(() => clearInterval(timer))
const help = computed(() => (supportContact ? t('forgot.help', { contact: supportContact }) : undefined))
</script>

<template>
  <AuthShell back :help="help">
    <div class="flex flex-col gap-2">
      <h1 class="text-[28px] leading-tight font-bold tracking-tight">{{ t('forgot.title') }}</h1>
      <p class="text-muted">{{ t('forgot.lead') }}</p>
    </div>
    <form class="flex flex-col gap-4" novalidate @submit.prevent="send">
      <Field v-model="email" :label="t('common.email')" type="email" autocomplete="email" inputmode="email" required />
      <Alert v-if="error">{{ error }}</Alert>
      <Button v-if="!sentTo" type="submit" variant="primary" size="lg" block :loading="busy" :disabled="!email">{{ t('forgot.submit') }}</Button>
    </form>
    <Card v-if="sentTo" class="flex flex-col gap-2 p-5" role="status">
      <span class="icon-tile mb-1 bg-ok-soft text-ok"><MailCheck class="size-5" aria-hidden="true" /></span>
      <p class="font-semibold">{{ t('forgot.sentTitle') }}</p>
      <p class="text-muted">{{ t('forgot.sent', { email: sentTo }) }}</p>
      <p class="text-[13px] text-muted">{{ wait > 0 ? t('forgot.resend', { seconds: wait }) : t('forgot.resendNow') }}</p>
      <Button v-if="wait === 0" size="sm" class="self-start" :loading="busy" @click="send">{{ t('forgot.again') }}</Button>
    </Card>
  </AuthShell>
</template>
