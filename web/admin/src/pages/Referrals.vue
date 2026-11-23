<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type Commission, type ReferralRules } from '@patato/api'
import { Badge, Button, Card, Empty, Field, LoadState, Section } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'
import { commissionStates } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const filters = [
  { value: '', label: '全部' },
  { value: 'frozen', label: '已冻结' },
  { value: 'pending', label: '待入账' },
  { value: 'settled', label: '已入账' },
  { value: 'cancelled', label: '已作废' },
]
const filter = computed(() => (typeof route.query.status === 'string' ? route.query.status : ''))
const rules = useQuery({ queryKey: ['referral', 'rules'], queryFn: () => api.get<ReferralRules>('/api/v1/referrals/settings') })
const list = useQuery({
  queryKey: ['referrals', filter],
  queryFn: () => api.get<{ items: Commission[]; totals: { pending: number; frozen: number; settled: number } }>('/api/v1/referrals', { query: { status: filter.value } }),
})

const editOpen = ref(false)
const form = ref({ enabled: false, rate: '', wait: '', first: false, subnet: '' })
function openEdit() {
  const r = rules.data.value
  if (!r) return
  form.value = { enabled: r.enabled, rate: String(r.rate_percent), wait: String(r.wait_days), first: r.first_order_only, subnet: String(r.subnet_limit) }
  editOpen.value = true
}
const editRequest = (): ChangeRequest => ({
  method: 'put', path: '/api/v1/referrals/settings',
  body: { enabled: form.value.enabled, rate_percent: Number(form.value.rate), wait_days: Number(form.value.wait), first_order_only: form.value.first, subnet_limit: Number(form.value.subnet) },
})

const releaseOpen = ref(false)
const releasing = ref<Commission>()
function release(c: Commission) {
  releasing.value = c
  releaseOpen.value = true
}
const releaseRequest = (): ChangeRequest => ({ method: 'post', path: '/api/v1/referrals/{id}:release', params: { id: releasing.value!.inviter_id }, body: { commission: releasing.value!.id } })
</script>

<template>
  <PageHeader title="邀请返佣" lead="被邀请人付款后返佣先待入账，等待期内没有退款才入账到邀请人余额，续费时抵扣。命中风控的自动冻结，解冻和改规则都要审批。" />
  <div class="flex flex-wrap items-start gap-7">
    <Section class="flex-[1_1_300px]" title="规则">
      <template #action><Button size="sm" :disabled="!rules.data.value" @click="openEdit">修改</Button></template>
      <LoadState :loading="rules.isPending.value" :error="rules.error.value" @retry="rules.refetch()">
        <Card v-if="rules.data.value" as="dl" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 p-5">
          <dt class="text-muted">返佣</dt><dd><Badge :tone="rules.data.value.enabled ? 'ok' : 'neutral'">{{ rules.data.value.enabled ? '开启' : '关闭' }}</Badge></dd>
          <dt class="text-muted">比例</dt><dd>实付金额的 {{ rules.data.value.rate_percent }}%</dd>
          <dt class="text-muted">入账</dt><dd>{{ rules.data.value.wait_days }} 天内没有退款</dd>
          <dt class="text-muted">只返首单</dt><dd>{{ rules.data.value.first_order_only ? '是' : '否' }}</dd>
          <dt class="text-muted">风控</dt><dd>{{ rules.data.value.subnet_limit ? `24 小时内同网段被邀请人超过 ${rules.data.value.subnet_limit} 个时冻结` : '不检查' }}</dd>
        </Card>
      </LoadState>
    </Section>
    <Section class="flex-[1_1_300px]" title="汇总">
      <Card v-if="list.data.value" as="dl" class="grid grid-cols-2 gap-x-4 gap-y-3 p-5">
        <dt class="text-muted">待入账</dt><dd class="text-right font-medium tabular">{{ fmt.money(list.data.value.totals.pending) }}</dd>
        <dt class="text-muted">已冻结</dt><dd class="text-right font-medium tabular">{{ fmt.money(list.data.value.totals.frozen) }}</dd>
        <dt class="text-muted">已入账</dt><dd class="text-right font-medium tabular">{{ fmt.money(list.data.value.totals.settled) }}</dd>
      </Card>
    </Section>
  </div>

  <Section title="返佣明细">
    <div role="tablist" aria-label="按状态筛选" class="seg-tabs">
      <button
        v-for="f in filters"
        :key="f.value"
        role="tab"
        type="button"
        :aria-selected="filter === f.value"
        class="seg-tab"
        @click="router.replace({ query: { status: f.value } })"
      >
        {{ f.label }}
      </button>
    </div>
    <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
      <Table :min-width="720" label="返佣明细">
        <thead><tr><th>邀请人</th><th>被邀请人</th><th>订单</th><th class="!text-right">金额</th><th>状态</th><th>说明</th><th /></tr></thead>
        <tbody>
          <tr v-for="c in list.data.value?.items ?? []" :key="c.id">
            <td class="break-all"><RouterLink :to="{ name: 'users', query: { id: String(c.inviter_id) } }">{{ fmt.maskEmail(c.inviter_email) }}</RouterLink></td>
            <td class="break-all">{{ c.invitee_email ? fmt.maskEmail(c.invitee_email) : '已删除' }}</td>
            <td><RouterLink :to="{ name: 'orders', query: { id: c.trade_no } }" class="font-mono text-[13px]">{{ c.trade_no }}</RouterLink></td>
            <td class="text-right tabular">{{ fmt.money(c.amount) }}</td>
            <td><Badge :tone="commissionStates[c.status]?.tone">{{ commissionStates[c.status]?.label }}</Badge></td>
            <td class="text-[13px] text-muted">{{ c.reason || (c.status === 'pending' ? `${fmt.date(c.release_at)} 入账` : '') }}</td>
            <td class="text-right"><Button v-if="c.status === 'frozen'" size="sm" @click="release(c)">解冻</Button></td>
          </tr>
          <tr v-if="(list.data.value?.items ?? []).length === 0"><td colspan="7"><Empty title="没有返佣记录" /></td></tr>
        </tbody>
      </Table>
    </LoadState>
  </Section>

  <ChangeDialog v-model:open="editOpen" title="修改返佣规则" description="只影响之后产生的返佣。涉及钱，需要审批。" :request="editRequest">
    <label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" class="accent-accent" />开启邀请返佣</label>
    <Field v-model="form.rate" label="返佣比例（%）" inputmode="numeric" />
    <Field v-model="form.wait" label="入账等待天数" hint="这么多天内没有退款才入账" inputmode="numeric" />
    <label class="flex items-center gap-2"><input v-model="form.first" type="checkbox" class="accent-accent" />只对被邀请人的首单返佣</label>
    <Field v-model="form.subnet" label="同网段冻结阈值" hint="24 小时内同一网段的被邀请人超过这个数时自动冻结，0 不检查" inputmode="numeric" />
  </ChangeDialog>
  <ChangeDialog v-if="releasing" v-model:open="releaseOpen" :title="`解冻返佣 ${fmt.money(releasing.amount)}`" :request="releaseRequest" confirm-only />
</template>
