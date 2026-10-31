<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { errorText } from '@patato/api'
import { Alert, Button, Card } from '@patato/ui'
import { login, passkeyError, supported } from '../lib/passkey'
import { useSession } from '../lib/session'

const route = useRoute()
const router = useRouter()
const session = useSession()
const error = ref('')
const busy = ref(false)

async function go() {
  error.value = ''
  busy.value = true
  try {
    await login()
    await session.load()
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/'
    router.replace(next)
  } catch (e) {
    error.value = passkeyError(e) ?? errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-dvh items-center justify-center px-4">
    <Card class="flex w-full max-w-[400px] flex-col gap-5 p-7">
      <div class="flex items-center gap-2.5">
        <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true"><path d="M4 12.5C3 7.5 7 3 12 3.5c4.6.4 7.2 4 6.4 8.4-.8 4.6-4.6 7.4-8.8 6.8C6.6 18.2 4.6 15.7 4 12.5Z" fill="#2B44C4" /></svg>
        <span class="text-lg font-semibold">patato</span>
      </div>
      <div class="flex flex-col gap-1">
        <h1 class="text-2xl font-semibold">运营者登录</h1>
        <p class="text-muted">用注册时绑定的 passkey 登录。网页登录后才能批准敏感变更。</p>
      </div>
      <Alert v-if="!supported()" tone="warn">这个浏览器不支持 passkey，换一个较新的浏览器再试。</Alert>
      <Alert v-if="error">{{ error }}</Alert>
      <Button variant="primary" size="lg" block :loading="busy" :disabled="!supported()" @click="go">用 passkey 登录</Button>
      <p class="text-[13px] text-muted">还没有账号？让现有运营者在服务器上运行 <code class="font-mono">patato-server operator invite -name 名字</code> 生成邀请链接。</p>
    </Card>
  </div>
</template>
