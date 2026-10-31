<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useInfiniteQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type Audit, type List } from '@patato/api'
import { Badge, Button, Empty, LoadState } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { entries, results, risks } from '../lib/labels'

const route = useRoute()
const router = useRouter()
const param = (k: string) => (typeof route.query[k] === 'string' ? (route.query[k] as string) : '')
const q = computed(() => param('q'))
const entry = computed(() => param('entry'))
const result = computed(() => param('result'))
const target = computed(() => param('target'))
const form = ref({ q: q.value, entry: entry.value, result: result.value })
watch([q, entry, result], () => (form.value = { q: q.value, entry: entry.value, result: result.value }))

function apply() {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...form.value, target: target.value })) if (v) next[k] = v.trim()
  router.replace({ query: next })
}

const list = useInfiniteQuery({
  queryKey: ['audit', q, entry, result, target],
  queryFn: ({ pageParam }) =>
    api.get<List<Audit>>('/api/v1/audit', { query: { q: q.value, entry: entry.value, result: result.value, target: target.value, cursor: pageParam || undefined, limit: 50 } }),
  initialPageParam: 0,
  getNextPageParam: (last) => last.next_cursor ?? undefined,
  refetchInterval: 30_000,
})
const rows = computed(() => list.data.value?.pages.flatMap((p) => p.items) ?? [])
const select = 'min-h-10 rounded-lg border border-control bg-card px-3 outline-none focus:border-accent'
</script>

<template>
  <PageHeader title="审计日志" lead="每一个写操作都在这里：谁、从哪个入口、做了什么、结果如何。" />
  <form role="search" class="flex flex-wrap items-end gap-3" @submit.prevent="apply">
    <label class="flex min-w-0 flex-[1_1_240px] flex-col gap-1">
      <span class="text-[13px] font-medium">搜索</span>
      <input v-model="form.q" type="search" placeholder="操作、对象或 change_id" :class="select" />
    </label>
    <label class="flex flex-col gap-1">
      <span class="text-[13px] font-medium">入口</span>
      <select v-model="form.entry" :class="select">
        <option value="">全部入口</option>
        <option v-for="(label, k) in entries" :key="k" :value="k">{{ label }}</option>
      </select>
    </label>
    <label class="flex flex-col gap-1">
      <span class="text-[13px] font-medium">结果</span>
      <select v-model="form.result" :class="select">
        <option value="">全部结果</option>
        <option v-for="(r, k) in results" :key="k" :value="k">{{ r.label }}</option>
      </select>
    </label>
    <Button type="submit">筛选</Button>
  </form>
  <p v-if="target" class="-mt-4 text-[13px] text-muted">
    只看对象 <span class="font-mono">{{ target }}</span>，<button type="button" class="text-accent" @click="router.replace({ query: { ...route.query, target: undefined } })">清除</button>
  </p>

  <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
    <div class="flex flex-col gap-3">
      <Table :min-width="900" label="审计日志">
        <thead>
          <tr><th>时间</th><th>操作</th><th>凭证</th><th>调用方自报</th><th>入口</th><th>风险</th><th>变更计划</th><th>结果</th></tr>
        </thead>
        <tbody>
          <tr v-for="a in rows" :key="a.id">
            <td class="whitespace-nowrap text-muted tabular">{{ fmt.date(a.at).slice(5) }} {{ fmt.time(a.at) }}</td>
            <td class="max-w-[320px]">
              {{ a.detail?.summary ?? a.action }}
              <p v-if="a.detail?.error" class="text-[13px] text-bad">{{ a.detail.error }}</p>
              <p v-else-if="a.detail?.reason" class="text-[13px] text-muted">{{ a.detail.reason }}</p>
            </td>
            <td class="text-[13px]">{{ a.subject }}</td>
            <td class="text-[13px] text-muted">{{ a.actor_claim || '—' }}</td>
            <td class="whitespace-nowrap">{{ entries[a.entry] ?? a.entry }}</td>
            <td><Badge :tone="risks[a.risk]?.tone">{{ risks[a.risk]?.label ?? a.risk }}</Badge></td>
            <td class="max-w-[9rem] truncate font-mono text-[13px]" :title="a.change_id">{{ a.change_id || '—' }}</td>
            <td><Badge :tone="results[a.result]?.tone">{{ results[a.result]?.label ?? a.result }}</Badge></td>
          </tr>
          <tr v-if="rows.length === 0"><td colspan="8"><Empty title="没有符合条件的记录" /></td></tr>
        </tbody>
      </Table>
      <Button v-if="list.hasNextPage.value" class="self-center" :loading="list.isFetchingNextPage.value" @click="list.fetchNextPage()">加载更多</Button>
    </div>
  </LoadState>
</template>
