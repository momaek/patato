<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Machine } from '@patato/api'
import { Badge, Empty, LoadState } from '@patato/ui'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { machineStates } from '../lib/labels'

const q = useQuery({ queryKey: ['machines'], queryFn: () => api.get<List<Machine>>('/api/v1/machines'), refetchInterval: 15_000 })
</script>

<template>
  <PageHeader title="机器" lead="一台机器跑一个 patato-node，承载机器上全部 patato-node 节点。往已接入的机器上加节点，推送后几秒内生效。" />
  <CliHint title="接入一台新机器" command="patato machine install-cmd tokyo-01">
    取得安装命令（含一小时内有效的一次性注册码），再用 tolato exec 在机器上原样执行：
  </CliHint>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <Table :min-width="860" label="机器列表">
      <thead>
        <tr><th>机器</th><th>接入状态</th><th>推送通道</th><th>节点</th><th>版本</th><th class="!text-right">最近联系</th></tr>
      </thead>
      <tbody>
        <tr v-for="m in q.data.value?.items ?? []" :key="m.host">
          <td>
            <span class="font-medium">{{ m.host }}</span>
            <p v-if="m.hostname && m.hostname !== m.host" class="text-[13px] text-muted">主机名 {{ m.hostname }}</p>
          </td>
          <td><Badge :tone="machineStates[m.status]?.tone">{{ machineStates[m.status]?.label ?? m.status }}</Badge></td>
          <td>{{ m.status === 'enrolled' ? (m.connected ? '已连接' : '未连接，轮询中') : '—' }}</td>
          <td>
            <template v-for="(n, i) in m.nodes" :key="n.id"><span v-if="i">、</span><RouterLink :to="{ name: 'node', params: { id: n.code } }">{{ n.name }}</RouterLink></template>
            <span v-if="m.nodes.length === 0" class="text-muted">—</span>
          </td>
          <td class="text-[13px]">
            <template v-if="m.agent_version">patato-node {{ m.agent_version }}<p class="text-muted">{{ m.kernel }}</p></template>
            <template v-else>—</template>
          </td>
          <td class="text-right whitespace-nowrap text-muted">{{ fmt.ago(m.last_seen_at) }}</td>
        </tr>
        <tr v-if="(q.data.value?.items ?? []).length === 0">
          <td colspan="6"><Empty title="还没有机器接入 patato-node">现有节点用 V2bX / XrayR 也能正常工作；需要精确计费和即时下发时再接入。</Empty></td>
        </tr>
      </tbody>
    </Table>
  </LoadState>
</template>
