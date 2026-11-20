<script setup lang="ts">
// 中转线路：每条线路经过哪些中转、各入口是否可用、今天和本月的流量、近 7 天曲线、每跳转发与探测。
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Plan, type RelayRoute, type RouteTraffic } from '@patato/api'
import { Badge, Button, Card, Empty, Field, LoadState, Section } from '@patato/ui'
import BarChart from '../components/BarChart.vue'
import ChangeDialog from '../components/ChangeDialog.vue'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'
import { entryTone, forwardStatus, hopStates } from '../lib/labels'

const q = useQuery({ queryKey: ['routes'], queryFn: () => api.get<List<RelayRoute>>('/api/v1/routes'), refetchInterval: 15_000 })
const traffic = useQuery({ queryKey: ['routes', 'traffic'], queryFn: () => api.get<RouteTraffic>('/api/v1/routes/traffic', { query: { days: 7 } }) })
const plans = useQuery({ queryKey: ['plans'], queryFn: () => api.get<List<Plan>>('/api/v1/plans') })
const selected = ref<string>()
const current = computed(() => (q.data.value?.items ?? []).find((r) => r.code === selected.value) ?? q.data.value?.items[0])
const series = computed(() => {
  const t = traffic.data.value?.items.find((x) => x.route.code === current.value?.code)
  return (t?.series ?? []).map((p) => ({ day: p.day, value: p.up + p.down }))
})

const op = ref<{ title: string; description?: string; confirm?: boolean; request: () => ChangeRequest }>()
const opOpen = ref(false)
const form = ref({ code: '', name: '', via: '', plan: '' })
function run(o: NonNullable<typeof op.value>) {
  op.value = o
  opOpen.value = true
}
function openCreate() {
  form.value = { code: '', name: '', via: '', plan: '' }
  run({
    title: '新建线路',
    description: '按客户端到落地的顺序列出中转机器（机器名与 tolato 一致）。线路本身不影响订阅。',
    request: () => ({ method: 'post', path: '/api/v1/routes', body: { code: form.value.code.trim(), name: form.value.name.trim(), hops: form.value.via.split(/[,，\s]+/).filter(Boolean) } }),
  })
}
function openApply(r: RelayRoute) {
  form.value.plan = plans.data.value?.items[0]?.code ?? ''
  run({
    title: `按套餐应用「${r.name}」`,
    description: '套餐里的每个节点经这条线路各开一个入口，端口由服务端在各跳分配。影响用户超过阈值时需要审批。',
    request: () => ({ method: 'post', path: '/api/v1/routes/{id}:apply', params: { id: r.code }, body: { plan: form.value.plan } }),
  })
}
const remove = (r: RelayRoute) => run({ title: `删除线路「${r.name}」`, confirm: true, request: () => ({ method: 'delete', path: '/api/v1/routes/{id}', params: { id: r.code } }) })
const pct = (v: number | null) => (v === null ? '—' : `${Math.round(v * 100)}%`)
</script>

<template>
  <PageHeader title="线路" lead="线路是有序的中转机器列表。给节点开中转入口后，Patato 在每一跳分配端口、下发转发规则并按线上字节统计流量。">
    <Button @click="openCreate">新建线路</Button>
  </PageHeader>
  <CliHint title="让一批节点走同一条线路" command="patato route apply gz-hk --plan premium --dry-run">先预演，看每一跳要占用的端口和受影响的用户数，再执行：</CliHint>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <Table :min-width="860" label="线路列表">
      <thead>
        <tr><th>线路</th><th>经过</th><th class="!text-right">节点</th><th>入口</th><th class="!text-right">今日</th><th class="!text-right">本月</th></tr>
      </thead>
      <tbody>
        <tr v-for="r in q.data.value?.items ?? []" :key="r.id" :class="current?.id === r.id ? 'bg-accent-soft/40' : ''">
          <td><button type="button" class="font-medium text-accent hover:text-accent-strong" @click="selected = r.code">{{ r.name }}</button><p class="font-mono text-[13px] text-muted">{{ r.code }}</p></td>
          <td>
            <template v-for="(h, i) in r.hops" :key="h.id">
              <span v-if="i" class="text-muted"> → </span>
              <span :class="h.state === 'offline' ? 'text-bad font-medium' : ''">{{ h.host }}</span>
            </template>
          </td>
          <td class="text-right tabular">{{ r.nodes }}</td>
          <td>
            <Badge :tone="r.entries.every((e) => !e.enabled || e.available) ? 'ok' : 'warn'">
              {{ r.entries.filter((e) => e.enabled && e.available).length }} / {{ r.entries.length }} 可用
            </Badge>
          </td>
          <td class="text-right tabular">{{ fmt.bytes(r.today_up + r.today_down) }}</td>
          <td class="text-right tabular">{{ fmt.bytes(r.month_up + r.month_down) }}</td>
        </tr>
        <tr v-if="(q.data.value?.items ?? []).length === 0">
          <td colspan="6"><Empty title="还没有线路">新建线路后，用「按套餐应用」或节点详情里的「加入口」让节点走中转。</Empty></td>
        </tr>
      </tbody>
    </Table>

    <div v-if="current" class="mt-8 flex flex-col gap-7">
      <Section :title="`${current.name} · 近 7 天流量`" meta="按第一跳的线上字节统计，与落地的用户流量分开展示">
        <template #action>
          <div class="flex gap-2">
            <Button size="sm" @click="openApply(current)">按套餐应用</Button>
            <Button size="sm" variant="danger" :disabled="current.entries.length > 0" @click="remove(current)">删除</Button>
          </div>
        </template>
        <Card class="p-5">
          <BarChart :label="`${current.name} 近 7 天每日流量`" :points="series" :format="fmt.bytes" />
        </Card>
      </Section>

      <Section title="每跳状态">
        <div class="flex flex-wrap gap-3">
          <Card v-for="(h, i) in current.hops" :key="h.id" class="flex min-w-[200px] flex-col gap-1 p-4">
            <span class="text-[13px] text-muted">第 {{ i + 1 }} 跳</span>
            <span class="font-medium">{{ h.host }}</span>
            <span class="font-mono text-[13px] text-muted">{{ h.address }}</span>
            <Badge :tone="hopStates[h.state].tone" class="self-start">{{ hopStates[h.state].label }}</Badge>
          </Card>
        </div>
      </Section>

      <Section title="入口与转发" :meta="`${current.entries.length} 个入口`">
        <Table :min-width="960" label="入口与各跳转发">
          <thead>
            <tr><th>入口</th><th>每跳转发</th><th>状态</th><th class="!text-right">24 小时探测</th><th class="!text-right">今日</th></tr>
          </thead>
          <tbody>
            <tr v-for="e in current.entries" :key="e.id">
              <td class="align-top">
                <RouterLink :to="{ name: 'node', params: { id: e.node_id } }" class="font-medium">{{ e.name }}</RouterLink>
                <p class="font-mono text-[13px] text-muted">{{ e.host }}:{{ e.port }}</p>
                <Badge :tone="entryTone(e)">{{ !e.enabled ? '停用' : e.available ? '可用' : '不可用' }}</Badge>
                <p v-if="e.enabled && !e.available" class="max-w-[260px] text-[13px] text-bad">{{ e.reason }}</p>
              </td>
              <td class="align-top font-mono text-[13px]">
                <p v-for="f in e.forwards" :key="f.id" class="flex h-6 items-center whitespace-nowrap">
                  {{ f.machine }}:{{ f.listen_port }}<span v-if="f.public_port !== f.listen_port" class="text-muted">（外网 {{ f.public_port }}）</span>
                  → {{ f.target_host }}<span v-if="f.target_ip && f.target_ip !== f.target_host" class="text-muted">（{{ f.target_ip }}）</span>:{{ f.target_port }}
                </p>
              </td>
              <td class="align-top text-[13px]">
                <p v-for="f in e.forwards" :key="f.id" class="flex h-6 items-center gap-1 whitespace-nowrap">
                  <Badge :tone="forwardStatus[f.status].tone">{{ forwardStatus[f.status].label }}</Badge>
                  <span v-if="f.applied_mode" class="text-muted">{{ f.applied_mode }}</span>
                  <span v-if="f.error" class="max-w-[220px] truncate text-bad" :title="f.error">{{ f.error }}</span>
                  <span v-else-if="f.applied_at" class="text-muted">{{ fmt.ago(f.applied_at) }}</span>
                </p>
              </td>
              <td class="align-top text-right text-[13px] tabular">
                <p v-for="f in e.forwards" :key="f.id" class="flex h-6 items-center justify-end whitespace-nowrap">
                  {{ pct(f.probe_success_rate_24h) }}<span v-if="f.probe_avg_ms_24h !== null" class="text-muted"> · {{ f.probe_avg_ms_24h }} ms</span>
                </p>
              </td>
              <td class="align-top text-right whitespace-nowrap tabular">{{ fmt.bytes(e.today_up + e.today_down) }}</td>
            </tr>
            <tr v-if="current.entries.length === 0">
              <td colspan="5"><Empty title="这条线路上还没有入口" /></td>
            </tr>
          </tbody>
        </Table>
      </Section>
    </div>
  </LoadState>

  <ChangeDialog v-if="op" v-model:open="opOpen" :title="op.title" :description="op.description" :request="op.request" :confirm-only="op.confirm">
    <template v-if="op.title === '新建线路'">
      <div class="grid grid-cols-2 gap-3">
        <Field v-model="form.code" label="代号" hint="小写字母、数字和连字符，例如 gz-hk" />
        <Field v-model="form.name" label="名称" hint="订阅里为「节点名 · 名称」" />
      </div>
      <Field v-model="form.via" label="经过的中转机器" hint="逗号分隔，按顺序，例如 gz-01, hk-01" />
    </template>
    <label v-else-if="op.title.startsWith('按套餐应用')" class="flex flex-col gap-1.5">
      <span class="font-medium">套餐</span>
      <select v-model="form.plan" class="field-input"><option v-for="p in plans.data.value?.items ?? []" :key="p.id" :value="p.code">{{ p.name }}（{{ fmt.num(p.user_count) }} 名用户）</option></select>
    </label>
  </ChangeDialog>
</template>
