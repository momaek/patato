<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type Audit, type List, type Node, type Status } from '@patato/api'
import { Badge, Button, Card, Empty, LoadState, Section, StatusDot, type Tone } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { results } from '../lib/labels'

const status = useQuery({ queryKey: ['status'], queryFn: () => api.get<Status>('/api/v1/status'), refetchInterval: 15_000 })
const audit = useQuery({ queryKey: ['audit', 'recent'], queryFn: () => api.get<List<Audit>>('/api/v1/audit', { query: { limit: 5 } }), refetchInterval: 30_000 })

const todoKinds: Record<string, { label: string; tone: Tone }> = {
  approval: { label: '待审批', tone: 'warn' },
  node_offline: { label: '节点离线', tone: 'bad' },
  panel_host_exec: { label: '面板主机执行', tone: 'bad' },
  user_over_quota: { label: '超额', tone: 'warn' },
  user_expired: { label: '到期', tone: 'neutral' },
}

// 离线和待接入的排在前面
const order: Record<string, number> = { offline: 0, pending: 1, online: 2 }
const nodes = computed(() => [...(status.data.value?.nodes.items ?? [])].sort((a, b) => order[a.state] - order[b.state] || a.name.localeCompare(b.name)).slice(0, 8))

function heartbeat(n: Node) {
  if (n.state === 'pending') return n.backend === 'native' ? '等待 patato-node 启动' : '等待首次心跳'
  return fmt.ago(n.last_heartbeat_at)
}

const panelTone: Record<string, Tone> = { ok: 'ok', alert: 'bad', unsupported: 'idle', error: 'bad', not_configured: 'idle' }
const panelLabel: Record<string, string> = { ok: '无远程执行', alert: '发现远程执行', unsupported: '无法检查', error: '检查失败', not_configured: '未配置' }

function todoLink(kind: string, ref: string) {
  if (kind === 'approval') return { name: 'approval', params: { id: ref } }
  if (ref.startsWith('node:')) return { name: 'node', params: { id: ref.slice(5) } }
  if (ref.startsWith('user:')) return { name: 'users', query: { id: ref.slice(5) } }
  return { name: 'audit' }
}
</script>

<template>
  <PageHeader title="总览" lead="日常操作交给 Agent 和 patato CLI，这里用来看状态、批变更。">
    <p v-if="status.data.value" class="text-[13px] text-muted">数据更新于 {{ fmt.time(status.data.value.updated_at) }}</p>
  </PageHeader>

  <LoadState :loading="status.isPending.value" :error="status.error.value" :message="status.error.value ? errorText(status.error.value) : undefined" @retry="status.refetch()">
    <template v-if="status.data.value">
      <Section title="需要你处理" :meta="`${status.data.value.todo.length} 件`">
        <Card as="ul" class="divide-y divide-line-soft">
          <li v-for="(item, i) in status.data.value.todo" :key="i" class="flex flex-wrap items-center gap-4 px-5 py-4">
            <Badge :tone="todoKinds[item.kind]?.tone ?? (item.severity === 'critical' ? 'bad' : 'warn')">{{ todoKinds[item.kind]?.label ?? '告警' }}</Badge>
            <div class="flex min-w-0 flex-[1_1_320px] flex-col gap-0.5">
              <p class="text-base font-medium">{{ item.title }}</p>
              <p v-if="item.detail" class="text-[13px] text-muted">{{ item.detail }}</p>
            </div>
            <span class="text-[13px] whitespace-nowrap text-muted">{{ item.expires_at ? fmt.until(item.expires_at) + '过期' : fmt.dateTime(item.at) }}</span>
            <RouterLink :to="todoLink(item.kind, item.ref)" class="no-underline">
              <Button :variant="i === 0 && item.kind === 'approval' ? 'primary' : 'secondary'" tabindex="-1">{{ item.kind === 'approval' ? '查看并审批' : '查看' }}</Button>
            </RouterLink>
          </li>
          <li v-if="status.data.value.todo.length === 0"><Empty title="没有需要处理的事">待审批的变更和告警会出现在这里，同时推送到 Telegram。</Empty></li>
        </Card>
      </Section>

      <div class="flex flex-wrap items-start gap-7">
        <Section class="flex-[2_1_520px]" title="节点状态" :meta="`${status.data.value.nodes.total} 个节点，${status.data.value.nodes.online} 个在线`">
          <template #action><RouterLink :to="{ name: 'nodes' }" class="font-medium">全部节点</RouterLink></template>
          <Table :min-width="560" label="节点状态">
            <thead>
              <tr><th>节点</th><th>状态</th><th class="!text-right">在线用户</th><th class="!text-right">今日流量</th><th class="!text-right">最近心跳</th></tr>
            </thead>
            <tbody>
              <tr v-for="n in nodes" :key="n.id">
                <td class="font-medium"><RouterLink :to="{ name: 'node', params: { id: n.code } }" class="text-ink no-underline hover:underline">{{ n.name }}</RouterLink></td>
                <td><StatusDot :state="n.state" /></td>
                <td class="text-right tabular">{{ n.state === 'pending' ? '—' : fmt.num(n.online_users) }}</td>
                <td class="text-right tabular">{{ n.state === 'pending' ? '—' : fmt.bytes(n.today_bytes) }}</td>
                <td class="text-right text-muted">{{ heartbeat(n) }}</td>
              </tr>
              <tr v-if="nodes.length === 0"><td colspan="5"><Empty title="还没有节点">对 Agent 说「把刚上线的东京机器加到高级套餐」，或用 patato node provision。</Empty></td></tr>
            </tbody>
          </Table>
        </Section>

        <div class="flex min-w-0 flex-[1_1_300px] flex-col gap-7">
          <Section title="今日用量">
            <Card as="dl" class="grid grid-cols-2 gap-x-4 gap-y-3 px-4.5 py-4">
              <dt class="text-muted">总流量</dt><dd class="text-right font-medium tabular">{{ fmt.bytes(status.data.value.usage.today_bytes) }}</dd>
              <dt class="text-muted">在线用户</dt><dd class="text-right font-medium tabular">{{ fmt.num(status.data.value.usage.online_users) }} 名</dd>
              <dt class="text-muted">超额停用</dt><dd class="text-right font-medium tabular">{{ fmt.num(status.data.value.users.over_quota ?? 0) }} 名用户</dd>
            </Card>
          </Section>
          <Section title="即将到期">
            <Card as="dl" class="grid grid-cols-2 gap-x-4 gap-y-3 px-4.5 py-4">
              <dt class="text-muted">今天</dt><dd class="text-right font-medium tabular">{{ fmt.num(status.data.value.expiring.today) }} 名用户</dd>
              <dt class="text-muted">3 天内</dt><dd class="text-right font-medium tabular">{{ fmt.num(status.data.value.expiring.days_3) }} 名用户</dd>
              <dt class="text-muted">7 天内</dt><dd class="text-right font-medium tabular">{{ fmt.num(status.data.value.expiring.days_7) }} 名用户</dd>
            </Card>
          </Section>
          <Card as="section" aria-labelledby="host-h" class="flex flex-col gap-2 px-4.5 py-4">
            <div class="flex items-center justify-between gap-2">
              <h2 id="host-h" class="text-[15px] font-semibold">面板主机</h2>
              <Badge :tone="panelTone[status.data.value.panel_host.status]">{{ panelLabel[status.data.value.panel_host.status] }}</Badge>
            </div>
            <p class="text-[13px] text-muted">{{ status.data.value.panel_host.message }}</p>
          </Card>
        </div>
      </div>
    </template>
  </LoadState>

  <Section title="最近操作">
    <template #action><RouterLink :to="{ name: 'audit' }" class="font-medium">审计日志</RouterLink></template>
    <LoadState :loading="audit.isPending.value" :error="audit.error.value" @retry="audit.refetch()">
      <Card as="ol" class="divide-y divide-line-soft">
        <li v-for="a in audit.data.value?.items ?? []" :key="a.id" class="flex flex-wrap gap-x-5 gap-y-1 px-4.5 py-3">
          <span class="w-24 text-muted tabular">{{ fmt.dateTime(a.at) }}</span>
          <span class="min-w-0 flex-[1_1_280px]">{{ a.actor_claim || a.subject }}：{{ a.detail?.summary ?? a.action }}</span>
          <Badge :tone="results[a.result]?.tone ?? 'neutral'">{{ results[a.result]?.label ?? a.result }}</Badge>
        </li>
        <li v-if="(audit.data.value?.items ?? []).length === 0"><Empty title="还没有操作记录" /></li>
      </Card>
    </LoadState>
  </Section>
</template>
