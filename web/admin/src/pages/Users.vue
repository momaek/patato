<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useInfiniteQuery, useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Plan, type User, type UserDetail } from '@patato/api'
import { Badge, Button, Card, Empty, Field, LoadState, Progress, Section } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import DestinationRanking from '../components/DestinationRanking.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'
import { entries, userStates } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const filters = [
  { value: '', label: '全部' },
  { value: 'active', label: '使用中' },
  { value: 'expiring', label: '7 天内到期' },
  { value: 'expired', label: '已到期' },
  { value: 'over_quota', label: '超额停用' },
  { value: 'banned', label: '已封禁' },
]
const filter = computed(() => (typeof route.query.state === 'string' ? route.query.state : ''))
const q = computed(() => (typeof route.query.q === 'string' ? route.query.q : ''))
const search = ref(q.value)
watch(q, (v) => (search.value = v))
const selected = computed(() => (typeof route.query.id === 'string' ? route.query.id : ''))
const reveal = ref(false)

function setQuery(patch: Record<string, string | undefined>) {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...route.query, ...patch })) if (typeof v === 'string' && v) next[k] = v
  router.replace({ query: next })
}

const list = useInfiniteQuery({
  queryKey: ['users', filter, q],
  queryFn: ({ pageParam }) =>
    api.get<List<User>>('/api/v1/users', {
      query: {
        q: q.value,
        state: filter.value === 'expiring' ? 'active' : filter.value,
        expiring_within_days: filter.value === 'expiring' ? 7 : undefined,
        cursor: pageParam || undefined,
        limit: 50,
      },
    }),
  initialPageParam: 0,
  getNextPageParam: (last) => last.next_cursor ?? undefined,
})
const users = computed(() => list.data.value?.pages.flatMap((p) => p.items) ?? [])
const detail = useQuery({
  queryKey: ['user', selected],
  queryFn: () => api.get<UserDetail>('/api/v1/users/{id}', { params: { id: selected.value } }),
  enabled: computed(() => !!selected.value),
})
const email = (e: string) => (reveal.value ? e : fmt.maskEmail(e))

// 手动操作：和 patato user 命令一样走变更计划
const plans = useQuery({ queryKey: ['plans'], queryFn: () => api.get<List<Plan>>('/api/v1/plans') })
type Op = { kind: 'edit' | 'create' | 'confirm'; title: string; description?: string; request: () => ChangeRequest }
const op = ref<Op>()
const opOpen = ref(false)
const edit = ref({ email: '', password: '', plan: '', extend: '', days: '30', transfer: '', devices: '', speed: '' })
function run(o: Op) {
  op.value = o
  opOpen.value = true
}
function openEdit(u: User) {
  edit.value = { email: u.email, password: '', plan: u.plan?.code ?? '', extend: '', days: '', transfer: String(Math.round(u.transfer_bytes / 1073741824)),
    devices: u.device_limit === null ? '' : String(u.device_limit), speed: u.speed_limit_mbps === null ? '' : String(u.speed_limit_mbps) }
  run({
    kind: 'edit', title: '修改用户', description: '续期从当前到期日接着算；换套餐会重置本周期流量。',
    request: () => {
      const e = edit.value
      const body: Record<string, unknown> = {}
      if (e.plan && e.plan !== u.plan?.code) body.plan = e.plan
      if (Number(e.extend) > 0) body.extend_days = Number(e.extend)
      if (e.transfer !== String(Math.round(u.transfer_bytes / 1073741824))) body.transfer_gb = Number(e.transfer)
      if (e.devices !== (u.device_limit === null ? '' : String(u.device_limit))) body.device_limit = Number(e.devices || 0)
      if (e.speed !== (u.speed_limit_mbps === null ? '' : String(u.speed_limit_mbps))) body.speed_limit_mbps = Number(e.speed || 0)
      return { method: 'patch', path: '/api/v1/users/{id}', params: { id: u.id }, body }
    },
  })
}
function openCreate() {
  edit.value = { email: '', password: '', plan: plans.data.value?.items[0]?.code ?? '', extend: '', days: '30', transfer: '', devices: '', speed: '' }
  run({
    kind: 'create', title: '新建用户', description: '不设密码时，用户用「找回密码」设置后登录用户侧。',
    request: () => {
      const e = edit.value
      const body: Record<string, unknown> = { email: e.email.trim(), plan: e.plan }
      if (e.password) body.password = e.password
      if (Number(e.days) > 0) body.days = Number(e.days)
      return { method: 'post', path: '/api/v1/users', body }
    },
  })
}
const confirm = (title: string, request: () => ChangeRequest, description?: string) => run({ kind: 'confirm', title, description, request })
function opDone(p: { status: string; kind: string }) {
  if (p.status === 'applied' && p.kind === 'user.delete') setQuery({ id: undefined })
}

function traffic(u: User) {
  return `${fmt.bytes(u.used_bytes)} / ${fmt.bytes(u.transfer_bytes)}`
}
function expiry(u: User) {
  if (!u.expires_at) return '长期'
  const days = Math.floor((new Date(u.expires_at).getTime() - Date.now()) / 86400000)
  if (days < 0) return fmt.date(u.expires_at)
  if (days === 0) return '今天到期'
  return fmt.date(u.expires_at)
}
</script>

<template>
  <PageHeader title="用户"><Button variant="primary" @click="openCreate">新建用户</Button></PageHeader>
  <div class="flex flex-col gap-3">
    <form role="search" class="flex flex-wrap gap-2" @submit.prevent="setQuery({ q: search.trim().replace(/^u_/, ''), id: undefined })">
      <label class="sr-only" for="user-q">搜索</label>
      <input
        id="user-q"
        v-model="search"
        type="search"
        placeholder="搜索邮箱或用户 ID"
        class="min-h-10 min-w-0 flex-[1_1_260px] rounded-lg border border-control bg-card px-3 outline-none focus:border-accent focus:ring-2 focus:ring-accent/20"
      />
      <Button type="submit">搜索</Button>
    </form>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div role="tablist" aria-label="按状态筛选" class="flex flex-wrap gap-1">
        <button
          v-for="f in filters"
          :key="f.value"
          role="tab"
          type="button"
          :aria-selected="filter === f.value"
          :class="['rounded-md px-3 py-1.5 font-medium', filter === f.value ? 'bg-ink text-white' : 'text-muted hover:bg-card']"
          @click="setQuery({ state: f.value, id: undefined })"
        >
          {{ f.label }}
        </button>
      </div>
      <label class="flex items-center gap-2 text-[13px] text-muted"><input v-model="reveal" type="checkbox" class="accent-accent" />显示完整邮箱</label>
    </div>
  </div>

  <div class="flex flex-wrap items-start gap-7">
    <div class="flex min-w-0 flex-[3_1_560px] flex-col gap-3">
      <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
        <Table :min-width="540" label="用户列表">
          <thead><tr><th>用户</th><th>套餐</th><th>状态</th><th class="!text-right">本月流量</th><th class="!text-right">到期</th></tr></thead>
          <tbody>
            <tr v-for="u in users" :key="u.id" :class="['cursor-pointer', String(u.id) === selected ? 'bg-accent-soft/60' : 'hover:bg-bg']" @click="setQuery({ id: String(u.id) })">
              <td>
                <button type="button" class="text-left font-medium text-accent" @click.stop="setQuery({ id: String(u.id) })">{{ email(u.email) }}</button>
                <p class="font-mono text-[13px] text-muted">u_{{ u.id }}</p>
              </td>
              <td>{{ u.plan?.name ?? '—' }}</td>
              <td><Badge :tone="userStates[u.state]?.tone">{{ userStates[u.state]?.label ?? u.state }}</Badge></td>
              <td class="text-right tabular">{{ traffic(u) }}</td>
              <td class="text-right tabular">{{ expiry(u) }}</td>
            </tr>
            <tr v-if="users.length === 0"><td colspan="5"><Empty title="没有找到用户">换个关键词或筛选条件试试。</Empty></td></tr>
          </tbody>
        </Table>
        <Button v-if="list.hasNextPage.value" class="self-center" :loading="list.isFetchingNextPage.value" @click="list.fetchNextPage()">加载更多</Button>
      </LoadState>
    </div>

    <Card v-if="selected" as="section" aria-labelledby="user-h" class="flex min-w-0 flex-[2_1_320px] flex-col gap-5 p-5">
      <LoadState :loading="detail.isPending.value" :error="detail.error.value" :message="detail.error.value ? errorText(detail.error.value) : undefined" @retry="detail.refetch()">
        <template v-if="detail.data.value">
          <div class="flex flex-col gap-1">
            <div class="flex items-center justify-between gap-2">
              <h2 id="user-h" class="text-lg font-semibold break-all">{{ email(detail.data.value.user.email) }}</h2>
              <Badge :tone="userStates[detail.data.value.user.state]?.tone">{{ userStates[detail.data.value.user.state]?.label }}</Badge>
            </div>
            <p class="text-[13px] text-muted"><span class="font-mono">u_{{ detail.data.value.user.id }}</span>，{{ fmt.date(detail.data.value.user.created_at) }} 创建</p>
          </div>
          <div class="flex flex-col gap-2">
            <div class="flex justify-between"><span class="text-muted">本月流量</span><span class="tabular">{{ fmt.bytes(detail.data.value.user.used_bytes) }} / {{ fmt.bytes(detail.data.value.user.transfer_bytes) }}</span></div>
            <Progress :value="detail.data.value.user.used_bytes" :max="detail.data.value.user.transfer_bytes" label="本月已用流量" />
            <p class="text-[13px] text-muted">上行 {{ fmt.bytes(detail.data.value.user.used_up) }}，下行 {{ fmt.bytes(detail.data.value.user.used_down) }}</p>
          </div>
          <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
            <dt class="text-muted">套餐</dt><dd>{{ detail.data.value.user.plan?.name ?? '无' }}</dd>
            <dt class="text-muted">到期</dt><dd class="tabular">{{ detail.data.value.user.expires_at ? fmt.date(detail.data.value.user.expires_at) : '长期' }}</dd>
            <dt class="text-muted">流量重置</dt><dd>{{ detail.data.value.user.next_reset_at ? fmt.monthDay(detail.data.value.user.next_reset_at) : '不重置' }}</dd>
            <dt class="text-muted">在线设备</dt>
            <dd class="tabular">{{ detail.data.value.user.devices_online }}<template v-if="detail.data.value.user.device_limit"> / {{ detail.data.value.user.device_limit }}</template> 台</dd>
            <dt class="text-muted">限速</dt><dd>{{ detail.data.value.user.speed_limit_mbps ? detail.data.value.user.speed_limit_mbps + ' Mbps' : '按套餐' }}</dd>
            <dt class="text-muted">订阅 token</dt><dd>{{ detail.data.value.user.token_reset_at ? fmt.dateTime(detail.data.value.user.token_reset_at) + ' 重置过' : '未重置过' }}</dd>
            <dt class="text-muted">余额</dt><dd class="tabular">{{ fmt.money(detail.data.value.user.balance) }}</dd>
            <template v-if="detail.data.value.user.invited_by"><dt class="text-muted">邀请人</dt><dd><button type="button" class="text-accent" @click="setQuery({ id: String(detail.data.value.user.invited_by) })">u_{{ detail.data.value.user.invited_by }}</button></dd></template>
          </dl>
          <div class="flex flex-wrap gap-2">
            <Button size="sm" @click="openEdit(detail.data.value.user)">续期或修改</Button>
            <Button size="sm" @click="confirm('重置流量', () => ({ method: 'post', path: '/api/v1/users/{id}:reset-traffic', params: { id: detail.data.value!.user.id } }))">重置流量</Button>
            <Button size="sm" @click="confirm('重置订阅链接', () => ({ method: 'post', path: '/api/v1/users/{id}:reset-token', params: { id: detail.data.value!.user.id } }), '旧链接立即失效，用户所有设备都要重新导入。')">重置订阅</Button>
            <Button size="sm" @click="confirm(detail.data.value.user.state === 'banned' ? '解封用户' : '封禁用户', () => ({ method: 'patch', path: '/api/v1/users/{id}', params: { id: detail.data.value!.user.id }, body: { banned: detail.data.value!.user.state !== 'banned' } }))">{{ detail.data.value.user.state === 'banned' ? '解封' : '封禁' }}</Button>
            <Button size="sm" variant="danger" @click="confirm('删除用户', () => ({ method: 'delete', path: '/api/v1/users/{id}', params: { id: detail.data.value!.user.id } }))">删除</Button>
          </div>
          <div class="flex gap-3 text-[13px]">
            <RouterLink :to="{ name: 'orders', query: { q: detail.data.value.user.email } }">订单</RouterLink>
            <RouterLink :to="{ name: 'referrals', query: {} }">邀请返佣</RouterLink>
          </div>
          <DestinationRanking view="user" :target="detail.data.value.user.id" />
          <Section title="最近记录">
            <ul class="flex flex-col gap-2 text-[13px]">
              <li v-for="a in detail.data.value.recent_audit" :key="a.id" class="flex gap-3">
                <span class="w-24 shrink-0 text-muted tabular">{{ fmt.dateTime(a.at) }}</span>
                <span>{{ a.detail?.summary ?? a.action }}，来自 {{ entries[a.entry] ?? a.entry }}</span>
              </li>
              <li v-if="detail.data.value.recent_audit.length === 0" class="text-muted">没有记录</li>
            </ul>
            <RouterLink :to="{ name: 'audit', query: { target: `user:${detail.data.value.user.id}` } }" class="text-[13px] font-medium">在审计日志中查看全部</RouterLink>
          </Section>
        </template>
      </LoadState>
    </Card>
  </div>

  <ChangeDialog v-if="op" v-model:open="opOpen" :title="op.title" :description="op.description" :request="op.request" :confirm-only="op.kind === 'confirm'" @done="opDone">
    <template v-if="op.kind === 'create'">
      <Field v-model="edit.email" label="邮箱" type="email" required />
      <Field v-model="edit.password" label="密码（可选）" type="password" autocomplete="new-password" hint="至少 8 位" />
      <Field v-model="edit.days" label="有效天数" hint="留空为长期有效" inputmode="numeric" />
    </template>
    <template v-else-if="op.kind === 'edit'">
      <Field v-model="edit.extend" label="续期天数" hint="留空不续期" inputmode="numeric" />
      <Field v-model="edit.transfer" label="每周期流量（GB）" inputmode="numeric" />
      <div class="grid grid-cols-2 gap-3">
        <Field v-model="edit.devices" label="设备数上限" hint="0 为不限" inputmode="numeric" />
        <Field v-model="edit.speed" label="限速（Mbps）" hint="0 为不限" inputmode="numeric" />
      </div>
    </template>
    <label v-if="op.kind !== 'confirm'" class="flex flex-col gap-1.5">
      <span class="font-medium">套餐</span>
      <select v-model="edit.plan" class="field-input"><option v-for="p in plans.data.value?.items ?? []" :key="p.id" :value="p.code">{{ p.name }}</option></select>
    </label>
  </ChangeDialog>
</template>
