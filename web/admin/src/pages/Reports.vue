<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type NodeCostReport, type RevenueReport, type TrafficReport } from '@patato/api'
import { Card, Empty, LoadState, Section, StatusDot } from '@patato/ui'
import BarChart from '../components/BarChart.vue'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'

const route = useRoute()
const router = useRouter()
const tabs = [
  { value: 'revenue', label: '收入' },
  { value: 'traffic', label: '流量' },
  { value: 'nodes', label: '节点成本' },
]
const tab = computed(() => (typeof route.query.tab === 'string' ? route.query.tab : 'revenue'))
const days = computed(() => Number(route.query.days ?? 30))
const set = (patch: Record<string, string>) => router.replace({ query: { ...route.query, ...patch } })

const revenue = useQuery({
  queryKey: ['report', 'revenue', days],
  queryFn: () => api.get<RevenueReport>('/api/v1/reports/revenue', { query: { days: days.value } }),
  enabled: computed(() => tab.value === 'revenue'),
})
const traffic = useQuery({
  queryKey: ['report', 'traffic', days],
  queryFn: () => api.get<TrafficReport>('/api/v1/reports/traffic', { query: { days: days.value } }),
  enabled: computed(() => tab.value === 'traffic'),
})
const nodes = useQuery({
  queryKey: ['report', 'nodes', days],
  queryFn: () => api.get<NodeCostReport>('/api/v1/reports/nodes', { query: { days: days.value } }),
  enabled: computed(() => tab.value === 'nodes'),
})
const current = computed(() => ({ revenue, traffic, nodes })[tab.value as 'revenue' | 'traffic' | 'nodes'] ?? revenue)
</script>

<template>
  <PageHeader title="报表" lead="收入按支付时间统计，退款按退款时间统计；节点成本按月成本折算到所选天数。" />
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div role="tablist" aria-label="报表" class="flex flex-wrap gap-1">
      <button
        v-for="t in tabs"
        :key="t.value"
        role="tab"
        type="button"
        :aria-selected="tab === t.value"
        :class="['rounded-md px-3 py-1.5 font-medium', tab === t.value ? 'bg-ink text-white' : 'text-muted hover:bg-card']"
        @click="set({ tab: t.value })"
      >
        {{ t.label }}
      </button>
    </div>
    <label class="flex items-center gap-2 text-[13px] text-muted">
      时间范围
      <select :value="String(days)" class="field-input" @change="set({ days: ($event.target as HTMLSelectElement).value })">
        <option value="7">最近 7 天</option>
        <option value="30">最近 30 天</option>
        <option value="90">最近 90 天</option>
        <option value="365">最近一年</option>
      </select>
    </label>
  </div>

  <LoadState :loading="current.isPending.value" :error="current.error.value" :message="current.error.value ? errorText(current.error.value) : undefined" @retry="current.refetch()">
    <template v-if="tab === 'revenue' && revenue.data.value">
      <Card as="dl" class="grid gap-4 p-5 sm:grid-cols-4">
        <div><dt class="text-muted">收入</dt><dd class="text-2xl font-semibold tabular">{{ fmt.money(revenue.data.value.revenue) }}</dd></div>
        <div><dt class="text-muted">退款</dt><dd class="text-2xl font-semibold tabular">{{ fmt.money(revenue.data.value.refunds) }}</dd></div>
        <div><dt class="text-muted">净收入</dt><dd class="text-2xl font-semibold tabular">{{ fmt.money(revenue.data.value.net) }}</dd></div>
        <div><dt class="text-muted">订单</dt><dd class="text-2xl font-semibold tabular">{{ fmt.num(revenue.data.value.orders) }}</dd></div>
      </Card>
      <Section title="每日收入" meta="实付金额，不含余额抵扣">
        <Card class="p-5">
          <BarChart label="每日收入" :points="revenue.data.value.series.map((d) => ({ day: d.day, value: d.revenue }))" :format="fmt.money" />
        </Card>
      </Section>
      <div class="flex flex-wrap items-start gap-7">
        <Section class="flex-[1_1_320px]" title="按套餐">
          <Table :min-width="320" label="按套餐汇总">
            <thead><tr><th>套餐</th><th class="!text-right">订单</th><th class="!text-right">收入</th></tr></thead>
            <tbody>
              <tr v-for="p in revenue.data.value.by_plan" :key="p.plan"><td>{{ p.plan }}</td><td class="text-right tabular">{{ p.orders }}</td><td class="text-right tabular">{{ fmt.money(p.revenue) }}</td></tr>
              <tr v-if="revenue.data.value.by_plan.length === 0"><td colspan="3"><Empty title="这段时间没有订单" /></td></tr>
            </tbody>
          </Table>
        </Section>
        <Section class="flex-[2_1_420px]" title="每日明细">
          <Table :min-width="420" label="每日收入明细">
            <thead><tr><th>日期</th><th class="!text-right">订单</th><th class="!text-right">收入</th><th class="!text-right">退款</th><th class="!text-right">余额抵扣</th></tr></thead>
            <tbody>
              <tr v-for="d in revenue.data.value.series.filter((x) => x.orders || x.refunds).reverse()" :key="d.day">
                <td class="tabular">{{ d.day }}</td><td class="text-right tabular">{{ d.orders }}</td><td class="text-right tabular">{{ fmt.money(d.revenue) }}</td>
                <td class="text-right tabular">{{ fmt.money(d.refunds) }}</td><td class="text-right tabular">{{ fmt.money(d.balance_used) }}</td>
              </tr>
              <tr v-if="!revenue.data.value.series.some((x) => x.orders || x.refunds)"><td colspan="5"><Empty title="这段时间没有订单" /></td></tr>
            </tbody>
          </Table>
        </Section>
      </div>
    </template>

    <template v-if="tab === 'traffic' && traffic.data.value">
      <Section title="每日流量" :meta="`共 ${fmt.bytes(traffic.data.value.total_bytes)}`">
        <Card class="p-5">
          <BarChart label="每日流量" :points="traffic.data.value.series.map((d) => ({ day: d.day, value: d.bytes }))" :format="fmt.bytes" />
        </Card>
      </Section>
      <div class="flex flex-wrap items-start gap-7">
        <Section class="flex-[1_1_320px]" title="用量最多的用户">
          <Table :min-width="320" label="用量最多的用户">
            <thead><tr><th>用户</th><th class="!text-right">流量</th></tr></thead>
            <tbody>
              <tr v-for="u in traffic.data.value.top_users" :key="u.user_id">
                <td class="break-all"><RouterLink :to="{ name: 'users', query: { id: String(u.user_id) } }">{{ fmt.maskEmail(u.email) }}</RouterLink></td>
                <td class="text-right tabular">{{ fmt.bytes(u.bytes) }}</td>
              </tr>
              <tr v-if="traffic.data.value.top_users.length === 0"><td colspan="2"><Empty title="没有用量" /></td></tr>
            </tbody>
          </Table>
        </Section>
        <Section class="flex-[1_1_320px]" title="每日明细">
          <Table :min-width="320" label="每日流量明细">
            <thead><tr><th>日期</th><th class="!text-right">上行</th><th class="!text-right">下行</th></tr></thead>
            <tbody>
              <tr v-for="d in [...traffic.data.value.series].reverse()" :key="d.day"><td class="tabular">{{ d.day }}</td><td class="text-right tabular">{{ fmt.bytes(d.up) }}</td><td class="text-right tabular">{{ fmt.bytes(d.down) }}</td></tr>
            </tbody>
          </Table>
        </Section>
      </div>
    </template>

    <template v-if="tab === 'nodes' && nodes.data.value">
      <Card as="dl" class="grid gap-4 p-5 sm:grid-cols-3">
        <div><dt class="text-muted">成本</dt><dd class="text-2xl font-semibold tabular">{{ fmt.money(nodes.data.value.total_cost) }}</dd></div>
        <div><dt class="text-muted">流量</dt><dd class="text-2xl font-semibold tabular">{{ fmt.bytes(nodes.data.value.total_bytes) }}</dd></div>
        <div>
          <dt class="text-muted">每 GB 成本</dt>
          <dd class="text-2xl font-semibold tabular">{{ nodes.data.value.total_bytes >= 1 << 30 ? fmt.money(Math.round(nodes.data.value.total_cost / (nodes.data.value.total_bytes / (1 << 30)))) : '—' }}</dd>
        </div>
      </Card>
      <Table :min-width="640" label="节点成本">
        <thead><tr><th>节点</th><th>状态</th><th class="!text-right">月成本</th><th class="!text-right">期间成本</th><th class="!text-right">流量</th><th class="!text-right">每 GB</th><th class="!text-right">用户</th></tr></thead>
        <tbody>
          <tr v-for="n in [...nodes.data.value.items].sort((a, b) => (b.cost_per_gb ?? -1) - (a.cost_per_gb ?? -1))" :key="n.id">
            <td><RouterLink :to="{ name: 'node', params: { id: n.code } }">{{ n.name }}</RouterLink><p class="text-[13px] text-muted">{{ n.region }}</p></td>
            <td><StatusDot :state="n.state" /></td>
            <td class="text-right tabular">{{ n.monthly_cost ? fmt.money(n.monthly_cost) : '未设置' }}</td>
            <td class="text-right tabular">{{ fmt.money(n.cost) }}</td>
            <td class="text-right tabular">{{ fmt.bytes(n.bytes) }}</td>
            <td class="text-right tabular">{{ n.cost_per_gb === null ? '—' : fmt.money(n.cost_per_gb) }}</td>
            <td class="text-right tabular">{{ fmt.num(n.users) }}</td>
          </tr>
          <tr v-if="nodes.data.value.items.length === 0"><td colspan="7"><Empty title="还没有节点" /></td></tr>
        </tbody>
      </Table>
      <CliHint title="设置节点成本" command="patato node update tokyo-01 --monthly-cost 35">在节点详情页修改，或在终端运行：</CliHint>
    </template>
  </LoadState>
</template>
