<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useInfiniteQuery, useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Order, type OrderDetail } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, Field, LoadState, Section } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { cents } from '../lib/change'
import { commissionStates, entries, orderKinds, orderStatuses } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const filters = [
  { value: '', label: '全部' },
  { value: 'paid', label: '已支付' },
  { value: 'pending', label: '待支付' },
  { value: 'refunding', label: '退款审批中' },
  { value: 'refunded', label: '已退款' },
  { value: 'cancelled', label: '已取消' },
]
const filter = computed(() => (typeof route.query.status === 'string' ? route.query.status : ''))
const q = computed(() => (typeof route.query.q === 'string' ? route.query.q : ''))
const search = ref(q.value)
watch(q, (v) => (search.value = v))
const selected = computed(() => (typeof route.query.id === 'string' ? route.query.id : ''))

function setQuery(patch: Record<string, string | undefined>) {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...route.query, ...patch })) if (typeof v === 'string' && v) next[k] = v
  router.replace({ query: next })
}

const list = useInfiniteQuery({
  queryKey: ['orders', filter, q],
  queryFn: ({ pageParam }) => api.get<List<Order>>('/api/v1/orders', { query: { status: filter.value, q: q.value, cursor: pageParam || undefined, limit: 50 } }),
  initialPageParam: 0,
  getNextPageParam: (last) => last.next_cursor ?? undefined,
})
const orders = computed(() => list.data.value?.pages.flatMap((p) => p.items) ?? [])
const detail = useQuery({
  queryKey: ['order', selected],
  queryFn: () => api.get<OrderDetail>('/api/v1/orders/{id}', { params: { id: selected.value } }),
  enabled: computed(() => !!selected.value),
})
const o = computed(() => detail.data.value?.order)
const statusOf = (x: Order) => (x.refund_pending ? 'refunding' : x.status)

// 退款
const refundOpen = ref(false)
const refund = ref({ amount: '', days: '', expireNow: false, keepBalance: false, reason: '' })
function openRefund() {
  refund.value = { amount: '', days: '', expireNow: false, keepBalance: false, reason: '' }
  refundOpen.value = true
}
function refundRequest() {
  const body: Record<string, unknown> = { reason: refund.value.reason.trim() }
  const amount = cents(refund.value.amount)
  if (amount !== undefined) body.amount = amount
  if (refund.value.days.trim()) body.deduct_days = Number(refund.value.days)
  if (refund.value.expireNow) body.expire_now = true
  if (refund.value.keepBalance) body.keep_balance = true
  return { method: 'post' as const, path: '/api/v1/orders/{id}:refund', params: { id: o.value!.trade_no }, body }
}

// 手动确认收款
const paidOpen = ref(false)
const paid = ref({ gateway: '', note: '' })
const paidRequest = () => ({ method: 'post' as const, path: '/api/v1/orders/{id}:mark-paid', params: { id: o.value!.trade_no },
  body: { gateway_trade_no: paid.value.gateway.trim(), note: paid.value.note.trim() } })

const cancelOpen = ref(false)
const cancelRequest = () => ({ method: 'post' as const, path: '/api/v1/orders/{id}:cancel', params: { id: o.value!.trade_no } })
</script>

<template>
  <PageHeader title="订单" lead="用户自助下单、支付回调验签后自动开通。退款和手动确认收款是敏感操作，需要审批。" />
  <div class="flex flex-col gap-3">
    <form role="search" class="flex flex-wrap gap-2" @submit.prevent="setQuery({ q: search.trim(), id: undefined })">
      <label class="sr-only" for="order-q">搜索</label>
      <input id="order-q" v-model="search" type="search" placeholder="搜索订单号或邮箱" class="field-input min-w-0 flex-[1_1_260px]" />
      <Button type="submit">搜索</Button>
    </form>
    <div role="tablist" aria-label="按状态筛选" class="flex flex-wrap gap-1">
      <button
        v-for="f in filters"
        :key="f.value"
        role="tab"
        type="button"
        :aria-selected="filter === f.value"
        :class="['rounded-md px-3 py-1.5 font-medium', filter === f.value ? 'bg-ink text-white' : 'text-muted hover:bg-card']"
        @click="setQuery({ status: f.value, id: undefined })"
      >
        {{ f.label }}
      </button>
    </div>
  </div>

  <div class="flex flex-wrap items-start gap-7">
    <div class="flex min-w-0 flex-[3_1_560px] flex-col gap-3">
      <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
        <Table :min-width="640" label="订单列表">
          <thead><tr><th>订单</th><th>用户</th><th>套餐</th><th class="!text-right">实付</th><th>状态</th><th class="!text-right">下单时间</th></tr></thead>
          <tbody>
            <tr v-for="x in orders" :key="x.id" :class="['cursor-pointer', x.trade_no === selected ? 'bg-accent-soft/60' : 'hover:bg-bg']" @click="setQuery({ id: x.trade_no })">
              <td><button type="button" class="font-mono text-[13px] text-accent" @click.stop="setQuery({ id: x.trade_no })">{{ x.trade_no }}</button></td>
              <td class="break-all">{{ fmt.maskEmail(x.email ?? '') }}</td>
              <td>{{ x.plan_name }} <span class="text-muted">{{ x.period_name }}</span></td>
              <td class="text-right tabular">{{ fmt.money(x.total) }}</td>
              <td><Badge :tone="orderStatuses[statusOf(x)]?.tone">{{ orderStatuses[statusOf(x)]?.label }}</Badge></td>
              <td class="text-right text-muted tabular">{{ fmt.dateTime(x.created_at) }}</td>
            </tr>
            <tr v-if="orders.length === 0"><td colspan="6"><Empty title="没有订单">用户在用户侧下单后出现在这里。</Empty></td></tr>
          </tbody>
        </Table>
        <Button v-if="list.hasNextPage.value" class="self-center" :loading="list.isFetchingNextPage.value" @click="list.fetchNextPage()">加载更多</Button>
      </LoadState>
    </div>

    <Card v-if="selected" as="section" aria-labelledby="order-h" class="flex min-w-0 flex-[2_1_340px] flex-col gap-5 p-5">
      <LoadState :loading="detail.isPending.value" :error="detail.error.value" :message="detail.error.value ? errorText(detail.error.value) : undefined" @retry="detail.refetch()">
        <template v-if="o && detail.data.value">
          <div class="flex flex-col gap-1">
            <div class="flex items-center justify-between gap-2">
              <h2 id="order-h" class="font-mono text-base font-semibold">{{ o.trade_no }}</h2>
              <Badge :tone="orderStatuses[statusOf(o)]?.tone">{{ orderStatuses[statusOf(o)]?.label }}</Badge>
            </div>
            <p class="text-[13px] text-muted">
              <RouterLink :to="{ name: 'users', query: { id: String(o.user_id) } }">{{ detail.data.value.user.email }}</RouterLink>，{{ orderKinds[o.kind] }}，{{ fmt.dateTime(o.created_at) }} 下单
            </p>
          </div>
          <Alert v-if="o.refund_pending" tone="warn">
            退款正在等待审批。<RouterLink v-if="o.refund_approval_id" :to="{ name: 'approval', params: { id: o.refund_approval_id } }">去审批</RouterLink>
          </Alert>
          <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
            <dt class="text-muted">套餐</dt><dd>{{ o.plan_name }}，{{ o.period_name }}</dd>
            <dt class="text-muted">原价</dt><dd class="tabular">{{ fmt.money(o.amount) }}</dd>
            <dt class="text-muted">优惠</dt><dd class="tabular">{{ fmt.money(o.discount) }}<template v-if="detail.data.value.coupon">（{{ detail.data.value.coupon.code }}）</template></dd>
            <dt class="text-muted">余额抵扣</dt><dd class="tabular">{{ fmt.money(o.balance_used) }}</dd>
            <dt class="text-muted">实付</dt><dd class="font-semibold tabular">{{ fmt.money(o.total) }}</dd>
            <dt class="text-muted">支付</dt><dd>{{ o.method || '—' }}<template v-if="o.gateway_trade_no">，流水 <span class="font-mono text-[13px]">{{ o.gateway_trade_no }}</span></template></dd>
            <dt class="text-muted">支付时间</dt><dd class="tabular">{{ fmt.dateTime(o.paid_at) }}</dd>
            <dt class="text-muted">顺延</dt><dd>{{ o.days }} 天；用户现在 {{ detail.data.value.user.expires_at ? fmt.date(detail.data.value.user.expires_at) + ' 到期' : '长期有效' }}</dd>
            <template v-if="o.status === 'refunded'"><dt class="text-muted">退款</dt><dd>{{ fmt.money(o.refund_amount) }}，扣回 {{ o.refund_days }} 天，{{ fmt.dateTime(o.refunded_at) }}</dd></template>
            <template v-if="detail.data.value.commission">
              <dt class="text-muted">返佣</dt>
              <dd>{{ fmt.money(detail.data.value.commission.amount) }}，<Badge :tone="commissionStates[detail.data.value.commission.status]?.tone">{{ commissionStates[detail.data.value.commission.status]?.label }}</Badge> {{ detail.data.value.commission.reason }}</dd>
            </template>
            <template v-if="o.note"><dt class="text-muted">备注</dt><dd>{{ o.note }}</dd></template>
            <dt class="text-muted">来源</dt><dd>{{ { portal: '用户侧', admin: 'Admin', xboard: 'Xboard 迁移' }[o.source] }}</dd>
          </dl>
          <div class="flex flex-wrap gap-2">
            <Button v-if="o.status === 'paid' && !o.refund_pending" variant="danger" @click="openRefund">退款</Button>
            <Button v-if="o.status === 'pending' || o.status === 'cancelled'" @click="paidOpen = true">确认已收款</Button>
            <Button v-if="o.status === 'pending'" @click="cancelOpen = true">取消订单</Button>
          </div>
          <Section v-if="detail.data.value.callbacks.length" title="支付回调">
            <ul class="flex flex-col gap-1.5 text-[13px]">
              <li v-for="cb in detail.data.value.callbacks" :key="cb.id" class="flex gap-3">
                <span class="w-24 shrink-0 text-muted tabular">{{ fmt.dateTime(cb.received_at) }}</span>
                <Badge :tone="cb.verified ? 'ok' : 'bad'">{{ cb.verified ? '验签通过' : '验签失败' }}</Badge>
                <span>{{ cb.result }}</span>
              </li>
            </ul>
          </Section>
          <Section title="记录">
            <ul class="flex flex-col gap-2 text-[13px]">
              <li v-for="a in detail.data.value.audit" :key="a.id" class="flex gap-3">
                <span class="w-24 shrink-0 text-muted tabular">{{ fmt.dateTime(a.at) }}</span>
                <span>{{ a.detail?.summary ?? a.action }}，来自 {{ entries[a.entry] ?? a.entry }}</span>
              </li>
              <li v-if="detail.data.value.audit.length === 0" class="text-muted">没有记录</li>
            </ul>
          </Section>
        </template>
      </LoadState>
    </Card>
  </div>

  <ChangeDialog v-if="o" v-model:open="refundOpen" title="退款" description="默认退实付全额、扣回这笔订单顺延的天数、退回抵扣的余额，并撤销返佣。资金需要在支付平台原路退回。" :request="refundRequest">
    <Field v-model="refund.amount" label="退款金额（元）" :placeholder="fmt.money(o.total)" hint="留空退实付全额" inputmode="numeric" />
    <Field v-model="refund.days" label="扣回天数" :placeholder="String(o.days)" hint="留空扣回这笔订单顺延的天数" inputmode="numeric" />
    <label class="flex items-center gap-2"><input v-model="refund.expireNow" type="checkbox" class="accent-accent" />订阅立即到期</label>
    <label class="flex items-center gap-2"><input v-model="refund.keepBalance" type="checkbox" class="accent-accent" />不退回下单时抵扣的余额</label>
    <Field v-model="refund.reason" label="原因" placeholder="例如：用户申请，节点长期不可用" />
  </ChangeDialog>
  <ChangeDialog v-if="o" v-model:open="paidOpen" title="确认已收款" description="支付回调丢失时使用：先在支付平台核对确实到账，再按订单开通。" :request="paidRequest">
    <Field v-model="paid.gateway" label="支付平台流水号" />
    <Field v-model="paid.note" label="备注" />
  </ChangeDialog>
  <ChangeDialog v-if="o" v-model:open="cancelOpen" title="取消订单" :request="cancelRequest" confirm-only />
</template>
