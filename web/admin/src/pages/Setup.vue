<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { errorText } from '@patato/api'
import { Alert, Button, Card } from '@patato/ui'
import { passkeyError, register, supported } from '../lib/passkey'
import { useSession } from '../lib/session'
import Logo from '../components/Logo.vue'

const route = useRoute()
const router = useRouter()
const session = useSession()
const invite = computed(() => (typeof route.query.invite === 'string' ? route.query.invite : ''))
const error = ref('')
const busy = ref(false)

async function go() {
  error.value = ''
  busy.value = true
  try {
    await register(invite.value)
    await session.load()
    router.replace({ name: 'overview' })
  } catch (e) {
    error.value = passkeyError(e) ?? errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-dvh items-center justify-center bg-[radial-gradient(ellipse_at_top,var(--color-accent-soft),transparent_60%)] px-4">
    <Card class="flex w-full max-w-[440px] flex-col gap-5 p-7 shadow-pop">
      <div class="flex items-center gap-2.5"><Logo /></div>
      <div class="flex flex-col gap-1">
        <h1 class="text-2xl font-semibold">注册 passkey</h1>
        <p class="text-muted">这条邀请链接 24 小时内有效，只能用一次。注册后用这把 passkey 登录 Admin、批准敏感变更。</p>
      </div>
      <Alert v-if="!invite">链接里没有邀请码。请打开 <code class="font-mono">patato-server operator invite</code> 输出的完整链接。</Alert>
      <Alert v-else-if="!supported()" tone="warn">这个浏览器不支持 passkey，换一个较新的浏览器再试。</Alert>
      <Alert v-if="error">{{ error }}</Alert>
      <Button variant="primary" size="lg" block :loading="busy" :disabled="!invite || !supported()" @click="go">创建 passkey</Button>
      <p class="text-[13px] text-muted">建议注册后在「账户与凭证」里再添加一把备用 passkey，丢了设备也能登录。</p>
    </Card>
  </div>
</template>
