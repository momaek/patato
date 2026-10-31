<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type ApprovalItem, type List, type Node, type Plan } from '@patato/api'
import { Badge, Card, Empty, LoadState, Section, StatusDot } from '@patato/ui'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { priceText, resetMethods } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const list = useQuery({ queryKey: ['plans'], queryFn: () => api.get<List<Plan>>('/api/v1/plans') })
const plans = computed(() => list.data.value?.items ?? [])
const selectedCode = computed(() => (typeof route.query.plan === 'string' ? route.query.plan : plans.value[0]?.code))
const detail = useQuery({
  queryKey: ['plan', selectedCode],
  queryFn: () => api.get<Plan>('/api/v1/plans/{id}', { params: { id: selectedCode.value! } }),
  enabled: computed(() => !!selectedCode.value),
})
// 节点状态和待审批的变更用于右侧详情：哪些节点在线、哪些改动在等批准
const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.get<List<Node>>('/api/v1/nodes') })
const pending = useQuery({ queryKey: ['approvals', 'pending'], queryFn: () => api.get<List<ApprovalItem>>('/api/v1/approvals', { query: { status: 'pending' } }) })
const nodeState = (id: number) => nodes.data.value?.items.find((n) => n.id === id)?.state ?? 'pending'
const totalUsers = computed(() => plans.value.reduce((s, p) => s + p.user_count, 0))

function price(p: Plan) {
  return priceText(p.prices)
}
function transfer(p: Plan) {
  return p.reset_method === 'never' ? fmt.gb(p.transfer_gb) : `${fmt.gb(p.transfer_gb)} / 月`
}
// 待审批里涉及这个套餐的：按摘要里的套餐名匹配（审批列表不带目标 ID）
const planPending = computed(() => {
  const p = detail.data.value
  if (!p) return []
  return (pending.data.value?.items ?? []).filter((a) => a.status === 'pending' && (a.summary.includes(p.name) || a.summary.includes(p.code)))
})
const pendingFor = (p: Plan) => (pending.data.value?.items ?? []).filter((a) => a.status === 'pending' && (a.summary.includes(p.name) || a.summary.includes(p.code))).length
</script>

<template>
  <PageHeader title="套餐" :lead="list.data.value ? `${plans.length} 个套餐，共 ${fmt.num(totalUsers)} 名用户` : undefined" />
  <CliHint title="要改价格或流量？" command="patato plan update premium --transfer-gb 400 --dry-run">
    对 Agent 说，或者在终端运行下面的命令。涉及价格的修改都需要审批。
  </CliHint>
  <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
    <div class="flex flex-wrap items-start gap-7">
      <div class="min-w-0 flex-[3_1_560px]">
        <Table :min-width="560" label="套餐列表">
          <thead>
            <tr><th>套餐</th><th>价格</th><th>流量</th><th>流量重置</th><th class="!text-right">节点</th><th class="!text-right">用户</th><th>待处理</th></tr>
          </thead>
          <tbody>
            <tr
              v-for="p in plans"
              :key="p.id"
              :class="['cursor-pointer', p.code === selectedCode ? 'bg-accent-soft/60' : 'hover:bg-bg']"
              @click="router.replace({ query: { plan: p.code } })"
            >
              <td>
                <button type="button" class="text-left font-medium text-accent" :aria-pressed="p.code === selectedCode" @click.stop="router.replace({ query: { plan: p.code } })">{{ p.name }}</button>
                <p class="font-mono text-[13px] text-muted">{{ p.code }}</p>
              </td>
              <td class="tabular">{{ price(p) }}</td>
              <td class="tabular">{{ transfer(p) }}</td>
              <td>{{ resetMethods[p.reset_method] }}</td>
              <td class="text-right tabular">{{ p.node_count }}</td>
              <td class="text-right tabular">{{ fmt.num(p.user_count) }}</td>
              <td><span v-if="pendingFor(p)" class="font-medium text-warn">{{ pendingFor(p) }} 项待审批</span><span v-else class="text-muted">—</span></td>
            </tr>
            <tr v-if="plans.length === 0"><td colspan="7"><Empty title="还没有套餐">用 patato plan create 创建，或从 Xboard 迁移。</Empty></td></tr>
          </tbody>
        </Table>
      </div>

      <Card v-if="detail.data.value" as="section" aria-labelledby="plan-h" class="flex min-w-0 flex-[2_1_320px] flex-col gap-5 p-5">
        <div class="flex flex-col gap-0.5">
          <h2 id="plan-h" class="text-xl font-semibold">{{ detail.data.value.name }}</h2>
          <p class="text-[13px] text-muted">版本 v{{ detail.data.value.version }}，{{ fmt.monthDay(detail.data.value.updated_at) }}修改</p>
        </div>
        <Section title="规则">
          <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
            <dt class="text-muted">每月流量</dt><dd class="tabular">{{ fmt.gb(detail.data.value.transfer_gb) }}</dd>
            <dt class="text-muted">流量重置</dt><dd>{{ detail.data.value.reset_method === 'never' ? '不重置' : '每月' + resetMethods[detail.data.value.reset_method] }}</dd>
            <dt class="text-muted">超额后</dt><dd>停用到下次重置</dd>
            <dt class="text-muted">到期后</dt><dd>停用订阅，保留账号</dd>
            <dt class="text-muted">设备数上限</dt><dd>{{ detail.data.value.device_limit ? detail.data.value.device_limit + ' 台' : '不限' }}</dd>
            <dt class="text-muted">限速</dt><dd>{{ detail.data.value.speed_limit_mbps ? detail.data.value.speed_limit_mbps + ' Mbps' : '不限' }}</dd>
            <dt class="text-muted">对外售卖</dt><dd>{{ detail.data.value.sellable ? '是' : '否' }}</dd>
          </dl>
        </Section>
        <Section title="挂载的节点">
          <ul class="flex flex-col divide-y divide-line-soft">
            <li v-for="n in detail.data.value.nodes ?? []" :key="n.id" class="flex items-center justify-between py-2">
              <RouterLink :to="{ name: 'node', params: { id: n.code } }">{{ n.name }}</RouterLink>
              <StatusDot :state="nodeState(n.id)" />
            </li>
            <li v-if="!detail.data.value.nodes?.length" class="py-2 text-muted">没有节点，这个套餐的用户订阅里是空的。</li>
          </ul>
        </Section>
        <Section v-if="planPending.length" title="待审批的变更">
          <ul class="flex flex-col gap-2">
            <li v-for="a in planPending" :key="a.approval_id" class="flex flex-col gap-0.5 rounded-lg border border-warn/25 bg-warn-soft/50 px-3 py-2">
              <RouterLink :to="{ name: 'approval', params: { id: a.approval_id } }" class="font-medium">{{ a.summary }}</RouterLink>
              <span class="text-[13px] text-muted">影响 {{ fmt.num(a.impact?.users) }} 名用户，{{ fmt.until(a.expires_at) }}过期</span>
            </li>
          </ul>
        </Section>
        <Badge v-if="detail.isFetching.value" tone="neutral" class="self-start">更新中</Badge>
      </Card>
    </div>
  </LoadState>
</template>
