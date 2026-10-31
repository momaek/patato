<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type CLIGrant } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'

const route = useRoute()
const code = computed(() => (typeof route.query.code === 'string' ? route.query.code : ''))
const q = useQuery({
  queryKey: ['cli-grant', code],
  queryFn: () => api.get<CLIGrant>('/api/v1/auth/cli/grants/{id}', { params: { id: code.value } }),
  enabled: computed(() => !!code.value),
})
const busy = ref<'approve' | 'deny' | ''>('')
const error = ref('')

async function decide(approve: boolean) {
  error.value = ''
  busy.value = approve ? 'approve' : 'deny'
  try {
    await api.post(approve ? '/api/v1/auth/cli/grants/{id}:approve' : '/api/v1/auth/cli/grants/{id}:deny', { params: { id: code.value } })
    await q.refetch()
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = ''
  }
}
const g = computed(() => q.data.value)
const scopes = { read: '只读：查询状态、预演', write: '可写：查询、预演和执行常规变更；敏感变更仍需你审批' }
</script>

<template>
  <PageHeader title="授权 patato CLI" lead="终端里的 patato auth login 正在请求一个 token。先核对短码和终端里显示的一致。" />
  <Alert v-if="!code">链接里没有授权码。请从终端打印的链接打开这一页。</Alert>
  <LoadState v-else :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <Card v-if="g" class="flex max-w-[520px] flex-col gap-4 p-6">
      <div class="flex flex-col gap-1">
        <span class="text-[13px] text-muted">短码</span>
        <span class="font-mono text-3xl font-medium tracking-[0.2em]">{{ g.user_code }}</span>
      </div>
      <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
        <dt class="text-muted">客户端</dt><dd>{{ g.client_name || 'patato CLI' }}</dd>
        <dt class="text-muted">权限</dt><dd>{{ scopes[g.scope] }}</dd>
        <dt class="text-muted">有效期</dt><dd>{{ fmt.until(g.expires_at) }}过期</dd>
      </dl>
      <Alert v-if="error">{{ error }}</Alert>
      <template v-if="g.status === 'pending'">
        <div class="flex flex-wrap gap-2">
          <Button variant="primary" :loading="busy === 'approve'" :disabled="!!busy" @click="decide(true)">批准</Button>
          <Button :loading="busy === 'deny'" :disabled="!!busy" @click="decide(false)">拒绝</Button>
        </div>
        <p class="text-[13px] text-muted">短码对不上、或者不是你刚刚发起的，点拒绝。token 可以随时在「账户与凭证」里吊销。</p>
      </template>
      <div v-else class="flex items-center gap-2">
        <Badge :tone="g.status === 'approved' || g.status === 'used' ? 'ok' : 'neutral'">
          {{ { approved: '已批准', used: '已批准', denied: '已拒绝', expired: '已过期', pending: '' }[g.status] }}
        </Badge>
        <span class="text-muted">{{ g.status === 'approved' || g.status === 'used' ? '回到终端，CLI 会自动拿到 token。' : '这次授权已结束。' }}</span>
      </div>
    </Card>
  </LoadState>
</template>
