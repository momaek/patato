<script setup lang="ts">
import { ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, isApiError, type List } from '@patato/api'
import { Alert, Badge, Button, Card, Code, Empty, LoadState, Section } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { addPasskey, passkeyError } from '../lib/passkey'
import { useSession } from '../lib/session'

interface Token { id: number; name: string; scope: string; created_at: string; last_used_at: string | null }
const session = useSession()
const tokens = useQuery({ queryKey: ['tokens'], queryFn: () => api.get<List<Token>>('/api/v1/auth/tokens') })
const busy = ref('')
const bind = ref<{ code: string; how: string } | null>(null)
const bindError = ref('')

async function revoke(t: Token) {
  if (!confirm(`吊销「${t.name}」？用它的 CLI 或 Agent 会立即失去访问权限。`)) return
  busy.value = 'revoke' + t.id
  try {
    await api.delete('/api/v1/auth/tokens/{id}', { params: { id: t.id } })
    toast.success('已吊销')
    tokens.refetch()
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = ''
  }
}

async function add() {
  busy.value = 'passkey'
  try {
    await addPasskey()
    toast.success('已添加新的 passkey')
  } catch (e) {
    toast.error(passkeyError(e) ?? errorText(e))
  } finally {
    busy.value = ''
  }
}

async function telegram() {
  bindError.value = ''
  busy.value = 'tg'
  try {
    bind.value = await api.post<{ code: string; how: string }>('/api/v1/auth/telegram:bind')
  } catch (e) {
    bindError.value = isApiError(e, 'telegram_not_configured') ? '服务端还没有配置 Telegram 机器人。' : errorText(e)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <PageHeader title="账户与凭证" :lead="`${session.me?.operator.name ?? ''}，这里管理你的 passkey、CLI token 和 Telegram 审批。`" />

  <Section title="CLI 与 Agent 的 token" meta="由 patato auth login 授权签发">
    <LoadState :loading="tokens.isPending.value" :error="tokens.error.value" @retry="tokens.refetch()">
      <Table :min-width="600" label="token 列表">
        <thead><tr><th>名称</th><th>权限</th><th>签发</th><th>最近使用</th><th /></tr></thead>
        <tbody>
          <tr v-for="t in tokens.data.value?.items ?? []" :key="t.id">
            <td class="font-medium">{{ t.name }}</td>
            <td><Badge :tone="t.scope === 'write' ? 'accent' : 'neutral'">{{ t.scope === 'write' ? '可写' : '只读' }}</Badge></td>
            <td class="tabular">{{ fmt.date(t.created_at) }}</td>
            <td class="text-muted">{{ t.last_used_at ? fmt.ago(t.last_used_at) : '没用过' }}</td>
            <td class="text-right"><Button size="sm" variant="danger" :loading="busy === 'revoke' + t.id" @click="revoke(t)">吊销</Button></td>
          </tr>
          <tr v-if="(tokens.data.value?.items ?? []).length === 0"><td colspan="5"><Empty title="没有 token">在终端运行 patato auth login 授权一台电脑。</Empty></td></tr>
        </tbody>
      </Table>
    </LoadState>
  </Section>

  <div class="flex flex-wrap gap-7">
    <Card as="section" class="flex flex-[1_1_320px] flex-col gap-3 p-5">
      <h2 class="text-lg font-semibold">passkey</h2>
      <p class="text-muted">再添加一把备用 passkey，比如另一台电脑或手机。丢了一台设备也能登录和审批。</p>
      <Button class="self-start" :loading="busy === 'passkey'" @click="add">添加 passkey</Button>
    </Card>
    <Card as="section" class="flex flex-[1_1_320px] flex-col gap-3 p-5">
      <h2 class="text-lg font-semibold">Telegram 审批</h2>
      <p class="text-muted">绑定后，敏感变更的审批卡片会推送到 Telegram，可以直接点按钮批准或拒绝。</p>
      <Alert v-if="bindError">{{ bindError }}</Alert>
      <template v-if="bind">
        <p>10 分钟内在 Telegram 里给机器人发送：</p>
        <Code :text="`/bind ${bind.code}`" block />
      </template>
      <Button v-else class="self-start" :loading="busy === 'tg'" @click="telegram">生成绑定码</Button>
    </Card>
  </div>
</template>
