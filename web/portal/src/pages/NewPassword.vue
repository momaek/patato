<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, errorText } from '@patato/api'
import { Alert, Button, Card, Field } from '@patato/ui'
import { CircleCheck } from 'lucide-vue-next'
import AuthShell from '../components/AuthShell.vue'

const { t } = useI18n()
const route = useRoute()
const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const password = ref('')
const confirm = ref('')
const touched = ref(false)
const error = ref('')
const busy = ref(false)
const done = ref(false)

const shortError = computed(() => (touched.value && password.value.length > 0 && password.value.length < 8 ? t('newPassword.tooShort') : ''))
// 确认框输到和新密码一样长还不一致就提示，不必等到提交
const mismatch = computed(() =>
  confirm.value !== '' && (touched.value || confirm.value.length >= password.value.length) && confirm.value !== password.value ? t('newPassword.mismatch') : '',
)

async function submit() {
  touched.value = true
  if (password.value.length < 8 || password.value !== confirm.value) return
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/v1/me/password:reset', { body: { token: token.value, password: password.value } })
    done.value = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthShell>
    <h1 class="text-[28px] leading-tight font-bold tracking-tight">{{ done ? t('newPassword.doneTitle') : t('newPassword.title') }}</h1>
    <Alert v-if="!token">{{ t('newPassword.missingToken') }}</Alert>
    <Card v-else-if="done" class="flex flex-col gap-3 p-5" role="status">
      <span class="icon-tile bg-ok-soft text-ok"><CircleCheck class="size-5" aria-hidden="true" /></span>
      <p class="text-muted">{{ t('newPassword.done') }}</p>
      <RouterLink :to="{ name: 'login' }" class="btn-primary self-start">{{ t('newPassword.toLogin') }}</RouterLink>
    </Card>
    <form v-else class="flex flex-col gap-4" novalidate @submit.prevent="submit">
      <Field v-model="password" :label="t('newPassword.password')" type="password" autocomplete="new-password" :hint="t('newPassword.hint')" :error="shortError" />
      <Field v-model="confirm" :label="t('newPassword.confirm')" type="password" autocomplete="new-password" :error="mismatch" />
      <Alert v-if="error">{{ error }}</Alert>
      <Button type="submit" variant="primary" size="lg" block :loading="busy">{{ t('newPassword.submit') }}</Button>
      <p class="text-[13px] text-muted">{{ t('newPassword.note') }}</p>
    </form>
  </AuthShell>
</template>
