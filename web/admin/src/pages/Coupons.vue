<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type Coupon, type List, type Plan } from '@patato/api'
import { Badge, Button, Empty, Field, LoadState } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { cents, periodKeys, type ChangeRequest } from '../lib/change'
import { couponStates, periods } from '../lib/labels'

const list = useQuery({ queryKey: ['coupons'], queryFn: () => api.get<List<Coupon>>('/api/v1/coupons') })
const plans = useQuery({ queryKey: ['plans'], queryFn: () => api.get<List<Plan>>('/api/v1/plans') })
const coupons = computed(() => list.data.value?.items ?? [])
const planName = (id: number) => plans.data.value?.items.find((p) => p.id === id)?.name ?? `#${id}`

function value(c: Coupon) {
  return c.kind === 'percent' ? `减 ${c.value}%` : `减 ${fmt.money(c.value)}`
}
function scope(c: Coupon) {
  const p = c.plan_ids.length ? c.plan_ids.map(planName).join('、') : '全部套餐'
  const d = c.periods.length ? c.periods.map((x) => periods[x] ?? x).join('、') : '全部周期'
  return `${p}，${d}`
}

const empty = () => ({ code: '', name: '', kind: 'amount' as 'amount' | 'percent', value: '', plans: [] as string[], periods: [] as string[], maxUses: '', perUser: '1', starts: '', ends: '' })
const form = ref(empty())
const editing = ref<Coupon>()
const open = ref(false)
function openCreate() {
  editing.value = undefined
  form.value = empty()
  open.value = true
}
function openEdit(c: Coupon) {
  editing.value = c
  form.value = {
    code: c.code, name: c.name, kind: c.kind, value: c.kind === 'amount' ? String(c.value / 100) : String(c.value),
    plans: c.plan_ids.map((id) => plans.data.value?.items.find((p) => p.id === id)?.code ?? String(id)), periods: [...c.periods],
    maxUses: c.max_uses ? String(c.max_uses) : '', perUser: c.per_user ? String(c.per_user) : '',
    starts: c.starts_at ? fmt.date(c.starts_at) : '', ends: c.ends_at ? fmt.date(c.ends_at) : '',
  }
  open.value = true
}
function toTime(v: string) {
  return v ? new Date(v + 'T00:00:00').toISOString() : undefined
}
function request(): ChangeRequest {
  const f = form.value
  const body: Record<string, unknown> = {
    name: f.name.trim(), kind: f.kind, value: f.kind === 'amount' ? cents(f.value) : Number(f.value), plans: f.plans, periods: f.periods,
    max_uses: Number(f.maxUses || 0), per_user: Number(f.perUser || 0),
  }
  if (f.starts) body.starts_at = toTime(f.starts)
  if (f.ends) body.ends_at = toTime(f.ends)
  if (editing.value) return { method: 'patch', path: '/api/v1/coupons/{id}', params: { id: editing.value.code }, body }
  return { method: 'post', path: '/api/v1/coupons', body: { ...body, code: f.code.trim() } }
}

const toggleOpen = ref(false)
const toggling = ref<Coupon>()
function toggle(c: Coupon) {
  toggling.value = c
  toggleOpen.value = true
}
const toggleRequest = (): ChangeRequest => ({ method: 'patch', path: '/api/v1/coupons/{id}', params: { id: toggling.value!.code }, body: { enabled: !toggling.value!.enabled } })
</script>

<template>
  <PageHeader title="优惠券" lead="发券、改券涉及钱，需要审批；只停用一张券不需要。">
    <Button variant="primary" @click="openCreate">新建优惠券</Button>
  </PageHeader>
  <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
    <Table :min-width="720" label="优惠券">
      <thead><tr><th>优惠码</th><th>优惠</th><th>适用</th><th class="!text-right">已用</th><th>状态</th><th>截止</th><th /></tr></thead>
      <tbody>
        <tr v-for="c in coupons" :key="c.id">
          <td><p class="font-mono font-medium">{{ c.code }}</p><p class="text-[13px] text-muted">{{ c.name }}</p></td>
          <td>{{ value(c) }}</td>
          <td class="text-[13px]">{{ scope(c) }}<template v-if="c.per_user">，每人 {{ c.per_user }} 次</template></td>
          <td class="text-right tabular">{{ c.used }}<template v-if="c.max_uses"> / {{ c.max_uses }}</template></td>
          <td><Badge :tone="couponStates[c.state]?.tone">{{ couponStates[c.state]?.label }}</Badge></td>
          <td class="text-muted tabular">{{ c.ends_at ? fmt.date(c.ends_at) : '不限' }}</td>
          <td class="text-right whitespace-nowrap">
            <Button size="sm" variant="ghost" @click="openEdit(c)">修改</Button>
            <Button size="sm" variant="ghost" @click="toggle(c)">{{ c.enabled ? '停用' : '启用' }}</Button>
          </td>
        </tr>
        <tr v-if="coupons.length === 0"><td colspan="7"><Empty title="还没有优惠券">新建后，用户在下单时输入优惠码即可使用。</Empty></td></tr>
      </tbody>
    </Table>
  </LoadState>

  <ChangeDialog v-model:open="open" :title="editing ? `修改优惠码 ${editing.code}` : '新建优惠券'" :request="request">
    <Field v-if="!editing" v-model="form.code" label="优惠码" hint="字母、数字、下划线和连字符，3–32 位；用户输入时不区分大小写" required />
    <Field v-model="form.name" label="名称" required />
    <div class="flex gap-3">
      <label class="flex flex-col gap-1.5">
        <span class="font-medium">类型</span>
        <select v-model="form.kind" class="field-input"><option value="amount">减金额</option><option value="percent">打折</option></select>
      </label>
      <Field v-model="form.value" class="flex-1" :label="form.kind === 'amount' ? '减免金额（元）' : '减免百分比（1–100）'" inputmode="numeric" required />
    </div>
    <fieldset class="flex flex-col gap-1.5">
      <legend class="mb-1.5 font-medium">适用套餐（不选为全部）</legend>
      <div class="flex flex-wrap gap-x-4 gap-y-1">
        <label v-for="p in plans.data.value?.items ?? []" :key="p.id" class="flex items-center gap-1.5"><input v-model="form.plans" type="checkbox" :value="p.code" class="accent-accent" />{{ p.name }}</label>
      </div>
    </fieldset>
    <fieldset class="flex flex-col gap-1.5">
      <legend class="mb-1.5 font-medium">适用周期（不选为全部）</legend>
      <div class="flex flex-wrap gap-x-4 gap-y-1">
        <label v-for="k in periodKeys" :key="k" class="flex items-center gap-1.5"><input v-model="form.periods" type="checkbox" :value="k" class="accent-accent" />{{ periods[k] }}</label>
      </div>
    </fieldset>
    <div class="grid grid-cols-2 gap-3">
      <Field v-model="form.maxUses" label="总次数" hint="留空不限" inputmode="numeric" />
      <Field v-model="form.perUser" label="每人次数" hint="留空不限" inputmode="numeric" />
      <Field v-model="form.starts" label="开始日期" type="date" />
      <Field v-model="form.ends" label="截止日期" type="date" />
    </div>
  </ChangeDialog>
  <ChangeDialog v-if="toggling" v-model:open="toggleOpen" :title="`${toggling.enabled ? '停用' : '启用'}优惠码 ${toggling.code}`" :request="toggleRequest" confirm-only />
</template>
