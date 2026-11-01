<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type ApprovalItem, type List, type Node, type Plan, type PlanRevision, type RuleProfile } from '@patato/api'
import { Badge, Button, Card, Empty, Field, LoadState, Section, StatusDot } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { cents, periodKeys, type ChangeRequest } from '../lib/change'
import { periods, priceText, resetMethods } from '../lib/labels'

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
const ruleProfiles = useQuery({ queryKey: ['rule-profiles'], queryFn: () => api.get<List<RuleProfile>>('/api/v1/rule-profiles') })
const ruleName = (code: string | null) => (code ? (ruleProfiles.data.value?.items.find((r) => r.code === code)?.name ?? code) : '内置规则')
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
const history = useQuery({
  queryKey: ['plan', selectedCode, 'revisions'],
  queryFn: () => api.get<{ items: PlanRevision[] }>('/api/v1/plans/{id}/revisions', { params: { id: selectedCode.value! } }),
  enabled: computed(() => !!selectedCode.value),
})

// 手动操作：新建、修改（含价格和灰度调价）、灰度转正、结束灰度、删除。涉及价格的都要审批。
type PriceForm = Record<string, string>
const yuan = (m?: Record<string, number>) => Object.fromEntries(periodKeys.map((k) => [k, m?.[k] !== undefined ? String(m[k] / 100) : '']))
function toCents(f: PriceForm) {
  const out: Record<string, number> = {}
  for (const k of periodKeys) {
    const c = cents(f[k] ?? '')
    if (c !== undefined) out[k] = c
  }
  return out
}
const form = ref({ code: '', name: '', transfer: '', reset: 'purchase_day', devices: '', speed: '', sellable: false, prices: yuan(), rollout: yuan(), percent: '', rules: '' })
const creating = ref(false)
const editOpen = ref(false)
function openForm(p?: Plan) {
  creating.value = !p
  form.value = p
    ? { code: p.code, name: p.name, transfer: String(p.transfer_gb), reset: p.reset_method, devices: String(p.device_limit || ''), speed: String(p.speed_limit_mbps || ''),
        sellable: p.sellable, prices: yuan(p.prices), rollout: yuan(p.rollout_prices), percent: p.rollout_percent ? String(p.rollout_percent) : '', rules: p.rule_profile ?? '' }
    : { code: '', name: '', transfer: '100', reset: 'purchase_day', devices: '', speed: '', sellable: false, prices: yuan(), rollout: yuan(), percent: '', rules: '' }
  editOpen.value = true
}
function formRequest(): ChangeRequest {
  const f = form.value
  const body: Record<string, unknown> = {
    name: f.name.trim(), transfer_gb: Number(f.transfer), reset_method: f.reset, device_limit: Number(f.devices || 0),
    speed_limit_mbps: Number(f.speed || 0), sellable: f.sellable, prices: toCents(f.prices),
  }
  if (creating.value ? f.rules : f.rules !== (detail.data.value?.rule_profile ?? '')) body.rule_profile = f.rules
  const roll = toCents(f.rollout)
  const percent = Number(f.percent || 0)
  if (Object.keys(roll).length && percent > 0) Object.assign(body, { rollout_prices: roll, rollout_percent: percent })
  else if (!creating.value && detail.data.value?.rollout_percent) body.end_rollout = true
  if (creating.value) return { method: 'post', path: '/api/v1/plans', body: { ...body, code: f.code.trim() } }
  return { method: 'patch', path: '/api/v1/plans/{id}', params: { id: f.code }, body }
}
const op = ref<{ title: string; request: () => ChangeRequest }>()
const opOpen = ref(false)
function run(title: string, request: () => ChangeRequest) {
  op.value = { title, request }
  opOpen.value = true
}
const pendingFor = (p: Plan) => (pending.data.value?.items ?? []).filter((a) => a.status === 'pending' && (a.summary.includes(p.name) || a.summary.includes(p.code))).length
</script>

<template>
  <PageHeader title="套餐" :lead="list.data.value ? `${plans.length} 个套餐，共 ${fmt.num(totalUsers)} 名用户。涉及价格的修改都需要审批。` : undefined">
    <Button variant="primary" @click="openForm()">新建套餐</Button>
  </PageHeader>
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
        <div class="flex flex-wrap gap-2">
          <Button size="sm" @click="openForm(detail.data.value)">修改</Button>
          <template v-if="detail.data.value.rollout_percent">
            <Button size="sm" @click="run('灰度价格转正', () => ({ method: 'patch', path: '/api/v1/plans/{id}', params: { id: detail.data.value!.code }, body: { promote_rollout: true } }))">灰度转正</Button>
            <Button size="sm" @click="run('结束灰度调价', () => ({ method: 'patch', path: '/api/v1/plans/{id}', params: { id: detail.data.value!.code }, body: { end_rollout: true } }))">结束灰度</Button>
          </template>
          <Button size="sm" variant="danger" @click="run('删除套餐', () => ({ method: 'delete', path: '/api/v1/plans/{id}', params: { id: detail.data.value!.code } }))">删除</Button>
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
            <dt class="text-muted">价格</dt><dd>{{ priceText(detail.data.value.prices) }}</dd>
            <dt class="text-muted">分流规则</dt>
            <dd>
              <RouterLink v-if="detail.data.value.rule_profile" :to="{ name: 'rules', query: { profile: detail.data.value.rule_profile } }">{{ ruleName(detail.data.value.rule_profile) }}</RouterLink>
              <span v-else>内置规则</span>
            </dd>
            <template v-if="detail.data.value.rollout_percent">
              <dt class="text-muted">灰度调价</dt><dd>约 {{ detail.data.value.rollout_percent }}% 的用户看到 {{ priceText(detail.data.value.rollout_prices) }}</dd>
            </template>
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
        <Section v-if="history.data.value?.items.length" title="历史版本" meta="每次修改前的设置">
          <ul class="flex flex-col gap-2 text-[13px]">
            <li v-for="r in history.data.value.items" :key="r.version" class="flex flex-col">
              <span class="font-medium">v{{ r.version }} <span class="font-normal text-muted">{{ fmt.dateTime(r.replaced_at) }} 被替换</span></span>
              <span class="text-muted">{{ r.snapshot.name }}，{{ fmt.gb(r.snapshot.transfer_gb) }}，{{ priceText(r.snapshot.prices) }}<template v-if="r.snapshot.rollout_percent">；灰度 {{ r.snapshot.rollout_percent }}%：{{ priceText(r.snapshot.rollout_prices) }}</template></span>
            </li>
          </ul>
        </Section>
        <Badge v-if="detail.isFetching.value" tone="neutral" class="self-start">更新中</Badge>
      </Card>
    </div>
  </LoadState>

  <ChangeDialog v-model:open="editOpen" :title="creating ? '新建套餐' : `修改套餐 ${form.name}`" :description="creating ? undefined : '改价格或灰度调价需要审批；已付款的订单不受影响。'" :request="formRequest">
    <Field v-if="creating" v-model="form.code" label="代号" hint="小写字母、数字和连字符，例如 premium" required />
    <Field v-model="form.name" label="名称" required />
    <div class="grid grid-cols-2 gap-3">
      <Field v-model="form.transfer" label="每周期流量（GB）" inputmode="numeric" />
      <label class="flex flex-col gap-1.5">
        <span class="font-medium">流量重置</span>
        <select v-model="form.reset" class="field-input"><option v-for="(v, k) in resetMethods" :key="k" :value="k">{{ v }}</option></select>
      </label>
      <Field v-model="form.devices" label="设备数上限" hint="留空不限" inputmode="numeric" />
      <Field v-model="form.speed" label="限速（Mbps）" hint="留空不限" inputmode="numeric" />
    </div>
    <label class="flex items-center gap-2"><input v-model="form.sellable" type="checkbox" class="accent-accent" />在用户侧售卖</label>
    <label class="flex flex-col gap-1.5">
      <span class="font-medium">默认分流规则</span>
      <select v-model="form.rules" class="field-input">
        <option value="">内置规则（国内直连，其余走代理）</option>
        <option v-for="r in ruleProfiles.data.value?.items ?? []" :key="r.code" :value="r.code">{{ r.name }}</option>
      </select>
      <span class="text-[13px] text-muted">用户可以在用户侧换成允许自选的其他规则。<RouterLink :to="{ name: 'rules' }">管理分流规则</RouterLink></span>
    </label>
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-1 font-medium">价格（元，留空表示不卖这个周期）</legend>
      <div class="grid grid-cols-2 gap-2">
        <label v-for="k in periodKeys" :key="k" class="flex items-center gap-2 text-[13px]">
          <span class="w-16 shrink-0 text-muted">{{ periods[k] }}</span>
          <input v-model="form.prices[k]" inputmode="decimal" class="field-input min-w-0 flex-1" />
        </label>
      </div>
    </fieldset>
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-1 font-medium">灰度调价（可选）</legend>
      <p class="text-[13px] text-muted">一部分用户先看到新价格，其余用户仍是原价；确认没问题后「灰度转正」。清空比例即结束灰度。</p>
      <Field v-model="form.percent" label="灰度比例（%）" inputmode="numeric" />
      <div class="grid grid-cols-2 gap-2">
        <label v-for="k in periodKeys" :key="k" class="flex items-center gap-2 text-[13px]">
          <span class="w-16 shrink-0 text-muted">{{ periods[k] }}</span>
          <input v-model="form.rollout[k]" inputmode="decimal" class="field-input min-w-0 flex-1" />
        </label>
      </div>
    </fieldset>
  </ChangeDialog>
  <ChangeDialog v-if="op" v-model:open="opOpen" :title="op.title" :request="op.request" confirm-only />
</template>
