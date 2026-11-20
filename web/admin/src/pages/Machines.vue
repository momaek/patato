<script setup lang="ts">
import { ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Machine, type PortRange } from '@patato/api'
import { Badge, Empty, Field, LoadState } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'
import { forwardModes, hopStates, machineStates } from '../lib/labels'

const q = useQuery({ queryKey: ['machines'], queryFn: () => api.get<List<Machine>>('/api/v1/machines'), refetchInterval: 15_000 })

const rangeText = (rs: PortRange[]) => rs.map((r) => (r.public_from ? `${r.local}:${r.public_from}` : r.local)).join(', ')
function parseRanges(s: string): PortRange[] {
  return s
    .split(/[,，\s]+/)
    .filter(Boolean)
    .map((x) => {
      const [local, pub] = x.split(':')
      if (!/^\d+(-\d+)?$/.test(local) || (pub !== undefined && !/^\d+$/.test(pub))) throw new Error(`端口段「${x}」格式应为 40000-49999 或 20000-20099:30000`)
      return pub ? { local, public_from: Number(pub) } : { local }
    })
}

// 设置网络属性：对外地址、端口段（NAT 映射）、转发模式。改已在使用的入口机器会改变订阅，转审批。
const open = ref(false)
const host = ref('')
const form = ref({ address: '', ranges: '', reserved: '', nat: false, mode: 'auto' })
function request(): ChangeRequest {
  return {
    method: 'patch',
    path: '/api/v1/machines/{id}/network',
    params: { id: host.value },
    body: {
      public_address: form.value.address.trim(),
      port_ranges: parseRanges(form.value.ranges),
      reserved_ports: parseRanges(form.value.reserved),
      nat: form.value.nat,
      forward_mode: form.value.mode,
    },
  }
}
function openSet(m: Machine) {
  const n = m.network
  host.value = m.host
  form.value = { address: n?.public_address ?? '', ranges: rangeText(n?.port_ranges ?? []), reserved: rangeText(n?.reserved_ports ?? []), nat: n?.nat ?? false, mode: n?.forward_mode ?? 'auto' }
  open.value = true
}
</script>

<template>
  <PageHeader title="机器" lead="一台机器跑一个 patato-node，承载机器上全部 patato-node 节点和中转转发。往已接入的机器上加节点或入口，推送后几秒内生效。" />
  <CliHint title="接入一台新机器" command="patato machine install-cmd tokyo-01">
    取得安装命令（含一小时内有效的一次性注册码），再用 tolato exec 在机器上原样执行：
  </CliHint>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <Table :min-width="1100" label="机器列表">
      <thead>
        <tr><th>机器</th><th>接入状态</th><th>推送通道</th><th>节点</th><th>网络</th><th>转发</th><th>版本</th><th class="!text-right">最近联系</th></tr>
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
            <template v-if="m.network">
              <p class="font-mono">{{ m.network.address }}</p>
              <p class="text-muted">
                <Badge v-if="m.network.nat" tone="warn">NAT</Badge>
                端口段 {{ rangeText(m.network.effective_port_ranges) || '未配置' }}
              </p>
              <button type="button" class="text-accent hover:text-accent-strong" @click="openSet(m)">设置</button>
            </template>
            <span v-else class="text-muted">—</span>
          </td>
          <td class="text-[13px]">
            <template v-if="m.network && m.network.forwards > 0">
              <p>{{ m.network.forwards }} 条<span v-if="m.network.failed_forwards" class="text-bad">，失败 {{ m.network.failed_forwards }}</span></p>
              <p class="text-muted">端口 {{ m.network.ports_used }} / {{ m.network.ports_total }}</p>
              <p class="text-muted tabular">今日 {{ fmt.bytes(m.network.today_up + m.network.today_down) }} · 本月 {{ fmt.bytes(m.network.month_up + m.network.month_down) }}</p>
              <p v-if="m.network.runtime" class="text-muted">实际模式 {{ m.network.runtime.mode }}</p>
              <p v-if="m.network.runtime?.error" class="text-bad">{{ m.network.runtime.error }}</p>
              <Badge v-if="m.network.state !== 'online'" :tone="hopStates[m.network.state].tone">{{ hopStates[m.network.state].label }}</Badge>
            </template>
            <span v-else class="text-muted">—</span>
          </td>
          <td class="text-[13px]">
            <template v-if="m.agent_version">patato-node {{ m.agent_version }}<p class="text-muted">{{ m.kernel }}</p></template>
            <template v-else>—</template>
          </td>
          <td class="text-right whitespace-nowrap text-muted">{{ fmt.ago(m.last_seen_at) }}</td>
        </tr>
        <tr v-if="(q.data.value?.items ?? []).length === 0">
          <td colspan="8"><Empty title="还没有机器接入 patato-node">现有节点用 V2bX / XrayR 也能正常工作；需要精确计费、即时下发或托管中转时再接入。</Empty></td>
        </tr>
      </tbody>
    </Table>
  </LoadState>

  <ChangeDialog
    v-model:open="open"
    :title="`设置 ${host} 的网络属性`"
    description="NAT 机器只有内网地址：填商家给的域名或外网 IP，并写明端口映射。修改已在使用的入口机器的对外地址或端口映射会改变订阅，需要审批。"
    :request="request"
  >
    <Field v-model="form.address" label="对外地址" hint="别的机器和客户端连它用的地址；留空用 tolato 上报的 IP。域名由服务端解析后下发" />
    <Field v-model="form.ranges" label="可分配端口段" hint="逗号分隔；NAT 映射写成 本机段:外网起点，例如 20000-20099:30000。留空为默认段 40000-49999" />
    <Field v-model="form.reserved" label="现有规则占用的端口段" hint="机器上手写 DNAT、socat 占用的端口，分配时避开，例如 60000-62000" />
    <label class="flex items-center gap-2"><input v-model="form.nat" type="checkbox" /> NAT 机器（只在上面的端口段内分配）</label>
    <label class="flex flex-col gap-1.5">
      <span class="font-medium">转发模式</span>
      <select v-model="form.mode" class="field-input"><option v-for="(label, k) in forwardModes" :key="k" :value="k">{{ label }}</option></select>
    </label>
  </ChangeDialog>
</template>
