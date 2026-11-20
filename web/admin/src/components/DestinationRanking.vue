<script setup lang="ts">
// 落地访问统计榜单：节点视图（按用户求和，可按用户筛选）或用户视图（可按节点筛选）。
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type DestinationRanking, type List, type Node } from '@patato/api'
import { Alert, Badge, LoadState, Section } from '@patato/ui'
import Table from './Table.vue'

const props = defineProps<{ view: 'node' | 'user'; target: string | number }>()

const periods = [
  { value: '1h', label: '1 小时' },
  { value: '24h', label: '24 小时' },
  { value: '72h', label: '3 天' },
  { value: '7d', label: '7 天' },
]
const since = ref('24h')
const filter = ref('') // 节点视图：用户 ID 或邮箱；用户视图：节点代号
const applied = ref('')
const nodes = useQuery({
  queryKey: ['nodes'],
  queryFn: () => api.get<List<Node>>('/api/v1/nodes'),
  enabled: computed(() => props.view === 'user'),
})
const q = useQuery({
  queryKey: ['destinations', () => props.view, () => props.target, since, applied],
  queryFn: () =>
    props.view === 'node'
      ? api.get<DestinationRanking>('/api/v1/nodes/{id}/destinations', { params: { id: props.target }, query: { since: since.value, user: applied.value } })
      : api.get<DestinationRanking>('/api/v1/users/{id}/destinations', { params: { id: props.target }, query: { since: since.value, node: applied.value } }),
  refetchInterval: 60_000,
})
const r = computed(() => q.data.value)
const kinds: Record<string, { label: string; tone: 'accent' | 'ok' | 'warn' }> = {
  domain: { label: '域名', tone: 'accent' },
  ip: { label: 'IP', tone: 'ok' },
  other: { label: '合并', tone: 'warn' },
}
const share = (b: number) => (r.value && r.value.total.bytes > 0 ? ((b / r.value.total.bytes) * 100).toFixed(1) + '%' : '—')
const meta = computed(() => (r.value ? `${fmt.bytes(r.value.total.bytes)}，${fmt.num(r.value.total.destinations)} 个目标，保留 ${r.value.retention_days} 天` : undefined))
</script>

<template>
  <Section title="访问目标" :meta="meta">
    <template #action>
      <div class="flex flex-wrap items-center gap-2 text-[13px]">
        <form v-if="view === 'node'" class="flex items-center gap-1.5" @submit.prevent="applied = filter.trim()">
          <input v-model="filter" class="field-input min-h-8 h-8 w-48 text-[13px]" placeholder="按用户筛选（ID 或邮箱）" aria-label="按用户筛选" />
          <button v-if="applied" type="button" class="text-muted hover:text-accent" @click="filter = ''; applied = ''">清除</button>
        </form>
        <select v-else v-model="applied" class="field-input min-h-8 h-8 text-[13px]" aria-label="按节点筛选">
          <option value="">全部节点</option>
          <option v-for="n in nodes.data.value?.items ?? []" :key="n.id" :value="n.code">{{ n.name }}</option>
        </select>
        <select v-model="since" class="field-input min-h-8 h-8 text-[13px]" aria-label="时间段">
          <option v-for="p in periods" :key="p.value" :value="p.value">最近 {{ p.label }}</option>
        </select>
      </div>
    </template>
    <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
      <template v-if="r">
        <Alert v-if="!r.enabled" tone="warn">
          落地访问统计没有开启（服务端配置 monitoring.destinations.enabled），patato-node 不采集新数据。开启后用户侧会看到统计范围与保留期限的说明。
        </Alert>
        <Table v-if="r.items.length" :min-width="560" label="访问目标">
          <thead>
            <tr>
              <th>目标</th>
              <th class="text-right">流量</th>
              <th class="text-right">占比</th>
              <th class="text-right">上行 / 下行</th>
              <th class="text-right">连接</th>
              <th class="text-right">{{ view === 'node' ? '用户' : '节点' }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in r.items" :key="d.kind + d.dest">
              <td>
                <span class="flex items-center gap-2">
                  <Badge :tone="kinds[d.kind]?.tone">{{ kinds[d.kind]?.label ?? d.kind }}</Badge>
                  <span class="font-mono text-[13px] break-all">{{ d.dest }}</span>
                  <span v-if="d.merged" class="text-[12px] text-muted">（{{ fmt.num(d.merged) }} 个目标）</span>
                </span>
              </td>
              <td class="text-right tabular">{{ fmt.bytes(d.bytes) }}</td>
              <td class="text-right tabular text-muted">{{ share(d.bytes) }}</td>
              <td class="text-right tabular text-muted">{{ fmt.bytes(d.up) }} / {{ fmt.bytes(d.down) }}</td>
              <td class="text-right tabular">{{ fmt.num(d.conns) }}</td>
              <td class="text-right tabular">{{ fmt.num(view === 'node' ? d.users ?? 0 : d.nodes ?? 0) }}</td>
            </tr>
          </tbody>
        </Table>
        <p v-else class="text-muted">这段时间没有访问记录。</p>
        <p class="text-[12px] text-muted">域名记主域名（例如 www.google.com 记为 google.com），不含路径；读不到域名时记 IP。每节点每 5 分钟最多 5000 个「用户 × 目标」，超出的合并为「其他」。</p>
      </template>
    </LoadState>
  </Section>
</template>
