<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { api, errorText } from '@patato/api'
import { Alert, Button, Field } from '@patato/ui'
import AuthShell from '../components/AuthShell.vue'
import { usePortalConfig } from '../session'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const qc = useQueryClient()
const config = usePortalConfig()
const email = ref('')
const password = ref('')
const invite = ref(typeof route.query.invite === 'string' ? route.query.invite : '')
const error = ref('')
const busy = ref(false)
const needInvite = computed(() => !!config.data.value?.registration_invite)

async function submit() {
  error.value = ''
  if (password.value.length < 8) {
    error.value = t('newPassword.tooShort')
    return
  }
  busy.value = true
  try {
    await api.post('/api/v1/me/register', { body: { email: email.value.trim(), password: password.value, invite_code: invite.value.trim() } })
    await qc.invalidateQueries({ queryKey: ['me'] })
    router.replace(config.data.value?.payment ? { name: 'buy' } : { name: 'home' })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthShell back>
    <div class="flex flex-col gap-2">
      <h1 class="text-[28px] leading-tight font-bold tracking-tight">{{ t('register.title') }}</h1>
      <p class="text-muted">{{ t('register.lead') }}</p>
    </div>
    <Alert v-if="config.data.value && !config.data.value.registration" tone="warn">{{ t('register.closed') }}</Alert>
    <form v-else class="flex flex-col gap-4" novalidate @submit.prevent="submit">
      <Field v-model="email" :label="t('common.email')" type="email" autocomplete="username" inputmode="email" required />
      <Field v-model="password" :label="t('common.password')" type="password" autocomplete="new-password" :hint="t('newPassword.hint')" required />
      <Field v-model="invite" :label="t('register.invite')" :hint="needInvite ? t('register.inviteRequired') : t('register.inviteHint')" :required="needInvite" />
      <Alert v-if="error">{{ error }}</Alert>
      <Button type="submit" variant="primary" size="lg" block :loading="busy" :disabled="!email || !password || (needInvite && !invite)">{{ t('register.submit') }}</Button>
    </form>
  </AuthShell>
</template>
