<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Node } from '@patato/api'
import { Empty, LoadState, StatusDot } from '@patato/ui'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { backends, protocols } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const q = useQuery({ queryKey: ['nodes'], queryFn: () => api.get<List<Node>>('/api/v1/nodes'), refetchInterval: 15_000 })
const all = computed(() => q.data.value?.items ?? [])
const count = (s?: string) => all.value.filter((n) => !s || n.state === s).length
const state = computed(() => (typeof route.query.state === 'string' ? route.query.state : ''))
const tabs = computed(() => [
  { value: '', label: '全部', n: count() },
  { value: 'online', label: '在线', n: count('online') },
  { value: 'offline', label: '离线', n: count('offline') },
  { value: 'pending', label: '待接入', n: count('pending') },
])
const rows = computed(() => all.value.filter((n) => !state.value || n.state === state.value))

function stateLabel(n: Node) {
  if (n.state === 'offline' && n.last_heartbeat_at) return `离线 ${fmt.ago(n.last_heartbeat_at).replace('前', '')}`
  return undefined
}
function heartbeat(n: Node) {
  if (n.state === 'pending') return n.backend === 'native' ? '等待 patato-node 启动' : '等待首次心跳'
  if (n.state === 'offline') return fmt.dateTime(n.last_heartbeat_at)
  return fmt.ago(n.last_heartbeat_at)
}
</script>

<template>
  <PageHeader title="节点" :lead="q.data.value ? `${count()} 个节点：${count('online')} 个在线，${count('offline')} 个离线，${count('pending')} 个待接入` : undefined" />
  <CliHint title="要上节点或改挂载？" command="patato node provision --host tokyo-03 --template jp-premium --plan premium --dry-run">
    对 Agent 说「把刚上线的东京机器加到高级套餐」，或者在终端运行：
  </CliHint>
  <div class="flex flex-col gap-3">
    <div role="tablist" aria-label="按状态筛选" class="seg-tabs">
      <button
        v-for="t in tabs"
        :key="t.value"
        role="tab"
        type="button"
        :aria-selected="state === t.value"
        class="seg-tab"
        @click="router.replace({ query: t.value ? { state: t.value } : {} })"
      >
        {{ t.label }} <span class="text-[12px] text-muted tabular">{{ t.n }}</span>
      </button>
    </div>
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <Table :min-width="980" label="节点列表">
        <thead>
          <tr>
            <th>节点</th><th>状态</th><th>协议</th><th>所属套餐</th><th class="!text-right">在线用户</th><th class="!text-right">今日流量</th><th>节点后端</th><th class="!text-right">最近心跳</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in rows" :key="n.id">
            <td>
              <RouterLink :to="{ name: 'node', params: { id: n.code } }" class="font-medium">{{ n.name }}</RouterLink>
              <p class="text-[13px] text-muted">{{ n.code }}<template v-if="n.region"> · {{ n.region }}</template></p>
            </td>
            <td><StatusDot :state="n.state" :label="stateLabel(n)" /></td>
            <td>{{ protocols[n.protocol] ?? n.protocol }}</td>
            <td>{{ n.plans.length ? n.plans.map((p) => p.name).join('、') : '—' }}</td>
            <td class="text-right tabular">{{ n.state === 'pending' ? '—' : fmt.num(n.online_users) }}</td>
            <td class="text-right tabular">{{ n.state === 'pending' ? '—' : fmt.bytes(n.today_bytes) }}</td>
            <td>{{ backends[n.backend] ?? n.backend }}</td>
            <td class="text-right whitespace-nowrap text-muted">{{ heartbeat(n) }}</td>
          </tr>
          <tr v-if="rows.length === 0"><td colspan="8"><Empty title="没有符合条件的节点" /></td></tr>
        </tbody>
      </Table>
    </LoadState>
  </div>
</template>
