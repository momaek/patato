<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type ApprovalItem, type List } from '@patato/api'
import { Badge, Empty, LoadState } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { approvalStates, risks } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const tabs = [
  { value: 'pending', label: '待审批' },
  { value: 'approved', label: '已批准' },
  { value: 'rejected', label: '已拒绝' },
  { value: '', label: '全部' },
]
const status = computed(() => (typeof route.query.status === 'string' ? route.query.status : 'pending'))
const q = useQuery({
  queryKey: ['approvals', status],
  queryFn: () => api.get<List<ApprovalItem>>('/api/v1/approvals', { query: { status: status.value, limit: 100 } }),
  refetchInterval: 15_000,
})
</script>

<template>
  <PageHeader title="审批" lead="敏感变更要运营者用 passkey 批准后才会执行。Telegram 卡片和这里的审批是同一份。" />
  <div role="tablist" aria-label="按状态筛选" class="flex flex-wrap gap-1">
    <button
      v-for="t in tabs"
      :key="t.value"
      role="tab"
      type="button"
      :aria-selected="status === t.value"
      :class="['rounded-md px-3 py-1.5 font-medium', status === t.value ? 'bg-ink text-white' : 'text-muted hover:bg-card']"
      @click="router.replace({ query: { status: t.value } })"
    >
      {{ t.label }}
    </button>
  </div>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <Table :min-width="760" label="审批列表">
      <thead>
        <tr><th>变更</th><th>风险</th><th class="!text-right">影响用户</th><th>发起方</th><th>状态</th><th class="!text-right">时间</th></tr>
      </thead>
      <tbody>
        <tr v-for="a in q.data.value?.items ?? []" :key="a.approval_id">
          <td class="max-w-[360px]">
            <RouterLink :to="{ name: 'approval', params: { id: a.approval_id } }" class="font-medium">{{ a.summary }}</RouterLink>
            <p v-if="a.risk_reason" class="text-[13px] text-muted">{{ a.risk_reason }}</p>
          </td>
          <td><Badge :tone="risks[a.risk]?.tone">{{ risks[a.risk]?.label ?? a.risk }}</Badge></td>
          <td class="text-right tabular">{{ fmt.num(a.impact?.users) }}</td>
          <td>{{ a.actor_claim || a.requester }}</td>
          <td><Badge :tone="approvalStates[a.status]?.tone">{{ approvalStates[a.status]?.label ?? a.status }}</Badge></td>
          <td class="text-right whitespace-nowrap text-muted">{{ a.status === 'pending' ? fmt.until(a.expires_at) + '过期' : fmt.dateTime(a.created_at) }}</td>
        </tr>
        <tr v-if="(q.data.value?.items ?? []).length === 0">
          <td colspan="6"><Empty :title="status === 'pending' ? '没有待审批的变更' : '没有记录'">Agent 或 CLI 发起的敏感变更会出现在这里。</Empty></td>
        </tr>
      </tbody>
    </Table>
  </LoadState>
</template>
