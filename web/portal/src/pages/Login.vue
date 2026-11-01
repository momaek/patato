<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText, isApiError } from '@patato/api'
import { Alert, Button, Field } from '@patato/ui'
import AuthShell from '../components/AuthShell.vue'
import { usePortalConfig } from '../session'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const qc = useQueryClient()
const email = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)
const config = usePortalConfig()

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/v1/me/session', { body: { email: email.value.trim(), password: password.value } })
    await qc.invalidateQueries({ queryKey: ['me'] })
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/'
    router.replace(next)
  } catch (e) {
    error.value = isApiError(e, 'invalid_credentials') ? t('login.failed') : errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthShell>
    <div class="flex flex-col gap-1.5">
      <h1 class="text-2xl font-semibold">{{ t('login.title') }}</h1>
      <p class="text-muted">{{ t('login.lead') }}</p>
    </div>
    <form class="flex flex-col gap-4" novalidate @submit.prevent="submit">
      <Field v-model="email" :label="t('common.email')" type="email" autocomplete="username" inputmode="email" required />
      <div class="flex flex-col gap-1.5">
        <Field v-model="password" :label="t('common.password')" type="password" autocomplete="current-password" required />
        <RouterLink :to="{ name: 'forgot', query: email ? { email } : {} }" class="self-end text-[14px] font-medium">{{ t('login.forgot') }}</RouterLink>
      </div>
      <Alert v-if="error">{{ error }}</Alert>
      <Button type="submit" variant="primary" size="lg" block :loading="busy" :disabled="!email || !password">{{ t('login.submit') }}</Button>
    </form>
    <RouterLink v-if="config.data.value?.registration" :to="{ name: 'register' }" class="self-center font-medium">{{ t('register.toRegister') }}</RouterLink>
  </AuthShell>
</template>
