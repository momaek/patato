<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useInfiniteQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Ticket } from '@patato/api'
import { Badge, Button, Empty, LoadState } from '@patato/ui'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { ticketStates } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const filters = [
  { value: 'open', label: '待回复' },
  { value: 'needs_human', label: '等你处理' },
  { value: 'replied', label: '已回复' },
  { value: 'closed', label: '已关闭' },
  { value: '', label: '全部' },
]
const filter = computed(() => (typeof route.query.status === 'string' ? route.query.status : 'open'))
const list = useInfiniteQuery({
  queryKey: ['tickets', filter],
  queryFn: ({ pageParam }) =>
    api.get<List<Ticket>>('/api/v1/tickets', {
      query: {
        status: filter.value === 'needs_human' ? undefined : filter.value,
        needs_human: filter.value === 'needs_human' ? 'true' : undefined,
        cursor: pageParam || undefined,
      },
    }),
  initialPageParam: 0,
  getNextPageParam: (last) => last.next_cursor ?? undefined,
  refetchInterval: 30_000,
})
const tickets = computed(() => list.data.value?.pages.flatMap((p) => p.items) ?? [])
</script>

<template>
  <PageHeader title="工单" lead="Agent 读工单、草拟回复；草稿由你在这里确认（可修改）后才发给用户。" />
  <CliHint title="让 Agent 先草拟" command="patato ticket list && patato ticket show <工单>">
    Agent 只能保存草稿（patato ticket draft），回答不了的会标为「等你处理」，不会直接回复用户。
  </CliHint>
  <div role="tablist" aria-label="按状态筛选" class="flex flex-wrap gap-1">
    <button
      v-for="f in filters"
      :key="f.value"
      role="tab"
      type="button"
      :aria-selected="filter === f.value"
      :class="['rounded-md px-3 py-1.5 font-medium', filter === f.value ? 'bg-ink text-white' : 'text-muted hover:bg-card']"
      @click="router.replace({ query: { status: f.value } })"
    >
      {{ f.label }}
    </button>
  </div>
  <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
    <Table :min-width="640" label="工单列表">
      <thead><tr><th>工单</th><th>用户</th><th>状态</th><th>标记</th><th class="!text-right">最近消息</th></tr></thead>
      <tbody>
        <tr v-for="t in tickets" :key="t.id" class="cursor-pointer hover:bg-bg" @click="router.push({ name: 'ticket', params: { id: t.id } })">
          <td>
            <RouterLink :to="{ name: 'ticket', params: { id: t.id } }" class="font-medium" @click.stop>{{ t.subject }}</RouterLink>
            <p class="font-mono text-[13px] text-muted">t_{{ t.id }}</p>
          </td>
          <td class="break-all">{{ fmt.maskEmail(t.email ?? '') }}</td>
          <td><Badge :tone="ticketStates[t.status]?.tone">{{ ticketStates[t.status]?.label }}</Badge></td>
          <td class="flex flex-wrap gap-1">
            <Badge v-if="t.needs_human" tone="warn">等你处理</Badge>
            <Badge v-if="t.has_draft" tone="accent">有草稿</Badge>
          </td>
          <td class="text-right text-muted tabular">{{ fmt.ago(t.last_message_at) }}</td>
        </tr>
        <tr v-if="tickets.length === 0"><td colspan="5"><Empty title="没有工单" /></td></tr>
      </tbody>
    </Table>
    <Button v-if="list.hasNextPage.value" class="self-center" :loading="list.isFetchingNextPage.value" @click="list.fetchNextPage()">加载更多</Button>
  </LoadState>
</template>
