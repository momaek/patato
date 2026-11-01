<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, type TicketDetail } from '@patato/api'
import { Alert, Badge, Button, Card, LoadState, Section, StatusDot } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import { orderStatuses, ticketStates, userStates } from '../lib/labels'

const route = useRoute()
const qc = useQueryClient()
const id = computed(() => String(route.params.id))
const q = useQuery({
  queryKey: ['ticket', id],
  queryFn: () => api.get<TicketDetail>('/api/v1/tickets/{id}', { params: { id: id.value } }),
  refetchInterval: 20_000,
})
const t = computed(() => q.data.value)

const body = ref('')
const fromDraft = ref(false)
const closeAfter = ref(false)
const busy = ref(false)
const error = ref('')
// 有草稿且还没开始写时，把草稿放进编辑框
watch(
  () => t.value?.draft?.body,
  (d) => {
    if (d && !body.value) {
      body.value = d
      fromDraft.value = true
    }
  },
  { immediate: true },
)

async function send() {
  error.value = ''
  busy.value = true
  try {
    const d = await api.post<TicketDetail>('/api/v1/tickets/{id}:reply', {
      params: { id: id.value },
      body: { body: body.value.trim(), from_draft: fromDraft.value && body.value.trim() !== '', close: closeAfter.value },
    })
    qc.setQueryData(['ticket', id], d)
    qc.invalidateQueries({ queryKey: ['tickets'] })
    body.value = ''
    fromDraft.value = false
    toast.success('回复已发送，用户会收到邮件通知')
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}

const closeOpen = ref(false)
const closeRequest = () => ({ method: 'post' as const, path: '/api/v1/tickets/{id}:close', params: { id: id.value } })
</script>

<template>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <template v-if="t">
      <PageHeader :title="t.ticket.subject">
        <template #crumbs>
          <p class="text-[13px] text-muted"><RouterLink :to="{ name: 'tickets' }">工单</RouterLink> / <span class="font-mono">t_{{ t.ticket.id }}</span></p>
          <div class="flex flex-wrap items-center gap-2">
            <Badge :tone="ticketStates[t.ticket.status]?.tone">{{ ticketStates[t.ticket.status]?.label }}</Badge>
            <Badge v-if="t.ticket.needs_human" tone="warn">等你处理</Badge>
          </div>
        </template>
        <Button v-if="t.ticket.status !== 'closed'" @click="closeOpen = true">关闭工单</Button>
      </PageHeader>

      <div class="flex flex-wrap items-start gap-7">
        <div class="flex min-w-0 flex-[2_1_520px] flex-col gap-6">
          <Alert v-if="t.ticket.needs_human" tone="warn">Agent 标为等你处理：{{ t.ticket.needs_human_reason }}</Alert>
          <ol class="flex flex-col gap-3">
            <li v-for="m in t.messages" :key="m.id" :class="['flex', m.author === 'operator' ? 'justify-end' : '']">
              <Card :class="'flex max-w-[85%] flex-col gap-1 px-4 py-3' + (m.author === 'operator' ? ' bg-accent-soft/50' : '')">
                <p class="text-[13px] text-muted">
                  {{ m.author === 'operator' ? `运营者 ${m.operator ?? ''}` : '用户' }} · {{ fmt.dateTime(m.created_at) }}
                  <template v-if="m.drafted_by">· 草稿来自 {{ m.drafted_by }}</template>
                </p>
                <p class="whitespace-pre-wrap">{{ m.body }}</p>
              </Card>
            </li>
          </ol>

          <Card as="section" aria-labelledby="reply-h" class="flex flex-col gap-3 p-5">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h2 id="reply-h" class="text-lg font-semibold">回复</h2>
              <span v-if="t.draft" class="text-[13px] text-muted">草稿由 {{ t.draft.drafted_by }} 于 {{ fmt.dateTime(t.draft.updated_at) }} 保存</span>
            </div>
            <div v-if="t.draft?.basis?.length" class="rounded-lg bg-bg px-3 py-2 text-[13px]">
              <p class="font-medium">草稿依据</p>
              <ul class="list-disc pl-5"><li v-for="b in t.draft.basis" :key="b">{{ b }}</li></ul>
            </div>
            <label class="sr-only" for="reply-body">回复内容</label>
            <textarea id="reply-body" v-model="body" rows="7" maxlength="5000" class="field-input py-2" placeholder="写给用户的回复" />
            <p v-if="fromDraft" class="text-[13px] text-muted">内容来自 Agent 草稿，发送前请核对：不要承诺需要审批的结果（退款、改价）。</p>
            <label class="flex items-center gap-2 text-[13px]"><input v-model="closeAfter" type="checkbox" class="accent-accent" />发送后关闭工单</label>
            <Alert v-if="error">{{ error }}</Alert>
            <div class="flex justify-end">
              <Button variant="primary" :loading="busy" :disabled="!body.trim()" @click="send">发送给用户</Button>
            </div>
          </Card>
        </div>

        <aside aria-label="用户上下文" class="flex min-w-0 flex-[1_1_300px] flex-col gap-6">
          <Card as="section" class="flex flex-col gap-3 p-5">
            <div class="flex items-center justify-between gap-2">
              <h2 class="text-lg font-semibold break-all">{{ t.context.user.email }}</h2>
              <Badge :tone="userStates[t.context.user.state]?.tone">{{ userStates[t.context.user.state]?.label }}</Badge>
            </div>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
              <dt class="text-muted">套餐</dt><dd>{{ t.context.user.plan?.name ?? '无' }}</dd>
              <dt class="text-muted">到期</dt><dd>{{ t.context.user.expires_at ? fmt.date(t.context.user.expires_at) : '长期' }}</dd>
              <dt class="text-muted">本月流量</dt><dd class="tabular">{{ fmt.bytes(t.context.user.used_bytes) }} / {{ fmt.bytes(t.context.user.transfer_bytes) }}</dd>
              <dt class="text-muted">余额</dt><dd class="tabular">{{ fmt.money(t.context.balance) }}</dd>
            </dl>
            <RouterLink :to="{ name: 'users', query: { id: String(t.context.user.id) } }" class="text-[13px] font-medium">查看用户</RouterLink>
          </Card>
          <Section title="最近订单">
            <ul class="flex flex-col gap-2 text-[13px]">
              <li v-for="o in t.context.orders" :key="o.id" class="flex flex-wrap items-center gap-2">
                <RouterLink :to="{ name: 'orders', query: { id: o.trade_no } }" class="font-mono">{{ o.trade_no }}</RouterLink>
                <span>{{ o.plan_name }} {{ o.period_name }}，{{ fmt.money(o.total) }}</span>
                <Badge :tone="orderStatuses[o.status]?.tone">{{ orderStatuses[o.status]?.label }}</Badge>
              </li>
              <li v-if="t.context.orders.length === 0" class="text-muted">没有订单</li>
            </ul>
          </Section>
          <Section title="套餐里的节点">
            <ul class="flex flex-col gap-1.5 text-[13px]">
              <li v-for="n in t.context.plan_nodes" :key="n.code" class="flex items-center justify-between gap-2">
                <RouterLink :to="{ name: 'node', params: { id: n.code } }">{{ n.name }}</RouterLink><StatusDot :state="n.state" />
              </li>
              <li v-if="t.context.plan_nodes.length === 0" class="text-muted">没有节点</li>
            </ul>
          </Section>
        </aside>
      </div>
      <ChangeDialog v-model:open="closeOpen" title="关闭工单" :request="closeRequest" confirm-only />
    </template>
  </LoadState>
</template>
