<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Node, type NodeEntry, type Plan, type RelayRoute } from '@patato/api'
import { Alert, Badge, Button, Card, Field, LoadState, Section, StatusDot } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import { cents, type ChangeRequest } from '../lib/change'
import { backends, entryKinds, entryTone, forwardStatus, protocols, show } from '../lib/labels'

const route = useRoute()
const id = computed(() => String(route.params.id))
const q = useQuery({
  queryKey: ['node', id],
  queryFn: () => api.get<Node>('/api/v1/nodes/{id}', { params: { id: id.value } }),
  refetchInterval: 10_000,
})
const n = computed(() => q.data.value)
const settings = computed(() => Object.entries(n.value?.settings ?? {}).filter(([, v]) => v !== null && v !== '' && !(typeof v === 'object' && Object.keys(v as object).length === 0)))
const router = useRouter()
const plans = useQuery({ queryKey: ['plans'], queryFn: () => api.get<List<Plan>>('/api/v1/plans') })
const unattached = computed(() => (plans.data.value?.items ?? []).filter((p) => !n.value?.plans.some((x) => x.id === p.id)))
const entries = useQuery({
  queryKey: ['node', id, 'entries'],
  queryFn: () => api.get<List<NodeEntry>>('/api/v1/nodes/{id}/entries', { params: { id: id.value } }),
  refetchInterval: 15_000,
})
const routes = useQuery({ queryKey: ['routes'], queryFn: () => api.get<List<RelayRoute>>('/api/v1/routes') })
const entryForm = ref({ route: '', suffix: '', address: '', port: '' })

// 手动操作都走变更计划：预演 → 执行，敏感时转审批
const op = ref<{ title: string; description?: string; confirm?: boolean; request: () => ChangeRequest }>()
const opOpen = ref(false)
const edit = ref({ name: '', address: '', port: '', serverPort: '', rate: '', tags: '', cost: '' })
const attachPlan = ref('')
function run(o: NonNullable<typeof op.value>) {
  op.value = o
  opOpen.value = true
}
function openEdit() {
  const x = n.value!
  edit.value = { name: x.name, address: x.address, port: String(x.port), serverPort: String(x.server_port), rate: String(x.rate), tags: x.tags.join(', '), cost: x.monthly_cost ? String(x.monthly_cost / 100) : '' }
  run({
    title: '修改节点',
    request: () => {
      const e = edit.value
      const body: Record<string, unknown> = {}
      if (e.name.trim() !== x.name) body.name = e.name.trim()
      if (e.address.trim() !== x.address) body.address = e.address.trim()
      if (Number(e.port) !== x.port) body.port = Number(e.port)
      if (Number(e.serverPort) !== x.server_port) body.server_port = Number(e.serverPort)
      if (Number(e.rate) !== x.rate) body.rate = Number(e.rate)
      const tags = e.tags.split(/[,，\s]+/).filter(Boolean)
      if (tags.join(',') !== x.tags.join(',')) body.tags = tags
      const c = cents(e.cost) ?? 0
      if (c !== x.monthly_cost) body.monthly_cost = c
      return { method: 'patch', path: '/api/v1/nodes/{id}', params: { id: x.code }, body }
    },
  })
}
function openAttach() {
  attachPlan.value = unattached.value[0]?.code ?? ''
  run({ title: '挂到套餐', description: '挂上后，这个套餐的用户订阅里会出现这个节点。影响用户超过阈值时需要审批。',
    request: () => ({ method: 'post', path: '/api/v1/nodes/{id}:attach', params: { id: n.value!.code }, body: { plan: attachPlan.value } }) })
}
const detach = (plan: string, name: string) =>
  run({ title: `从「${name}」卸载`, confirm: true, request: () => ({ method: 'post', path: '/api/v1/nodes/{id}:detach', params: { id: n.value!.code }, body: { plan } }) })
function openEntryAdd() {
  entryForm.value = { route: routes.data.value?.items[0]?.code ?? '', suffix: '', address: '', port: '' }
  run({
    title: '加入口',
    description: '经线路的入口由服务端在每一跳分配端口并生成转发，全部生效后出现在订阅里；不选线路、只填地址和端口是自定义入口（例如 IPLC）。',
    request: () => {
      const f = entryForm.value
      const body: Record<string, unknown> = {}
      if (f.route) body.route = f.route
      if (f.suffix.trim()) body.suffix = f.suffix.trim()
      if (f.address.trim()) body.address = f.address.trim()
      if (f.port.trim()) body.port = Number(f.port)
      return { method: 'post', path: '/api/v1/nodes/{id}/entries', params: { id: n.value!.code }, body }
    },
  })
}
const entryAction = (e: NodeEntry, action: 'enable' | 'disable' | 'delete') =>
  run({
    title: `${{ enable: '启用', disable: '停用', delete: '删除' }[action]}入口「${e.name}」`,
    confirm: true,
    request: () =>
      action === 'delete'
        ? { method: 'delete', path: '/api/v1/nodes/{id}/entries/{entry}', params: { id: n.value!.code, entry: e.id } }
        : { method: 'post', path: `/api/v1/nodes/{id}/entries/{entry}:${action}`, params: { id: n.value!.code, entry: e.id } },
  })
const rotate = () => run({ title: '轮换节点密钥', confirm: true, request: () => ({ method: 'post', path: '/api/v1/nodes/{id}:rotate-key', params: { id: n.value!.code } }) })
const remove = () => run({ title: '删除节点', confirm: true, request: () => ({ method: 'delete', path: '/api/v1/nodes/{id}', params: { id: n.value!.code } }) })
function done(p: { status: string; kind: string }) {
  if (p.status === 'applied' && p.kind === 'node.delete') router.push({ name: 'nodes' })
}
</script>

<template>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <template v-if="n">
      <PageHeader :title="n.name">
        <template #crumbs>
          <p class="text-[13px] text-muted"><RouterLink :to="{ name: 'nodes' }">节点</RouterLink> / <span class="font-mono">{{ n.code }}</span></p>
        </template>
        <StatusDot :state="n.state" class="text-base" />
      </PageHeader>

      <Alert v-if="n.runtime && !n.runtime.running" role="alert">patato-node 报告这个节点没有跑起来：{{ n.runtime.error || '原因未知' }}</Alert>
      <Alert v-else-if="n.state === 'pending'" tone="warn">
        节点还没有接入，暂不出现在用户订阅里。
        <template v-if="n.backend === 'native'">机器 {{ n.host }} 上的 patato-node 收到推送并启动节点后会自动转为在线。</template>
        <template v-else>在机器上安装节点后端，收到首次心跳后自动转为在线。</template>
      </Alert>

      <div class="flex flex-wrap items-start gap-7">
        <div class="flex min-w-0 flex-[2_1_480px] flex-col gap-7">
          <Section title="概况">
            <Card as="dl" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 p-5 sm:grid-cols-[auto_1fr_auto_1fr]">
              <dt class="text-muted">协议</dt><dd>{{ protocols[n.protocol] ?? n.protocol }}</dd>
              <dt class="text-muted">节点后端</dt><dd>{{ backends[n.backend] }}</dd>
              <dt class="text-muted">地址</dt><dd class="font-mono text-[13px]">{{ n.address }}:{{ n.port }}</dd>
              <dt class="text-muted">机器</dt>
              <dd><RouterLink v-if="n.host && n.backend === 'native'" :to="{ name: 'machines' }">{{ n.host }}</RouterLink><template v-else>{{ n.host || '—' }}</template></dd>
              <dt class="text-muted">地区</dt><dd>{{ n.region || '—' }}</dd>
              <dt class="text-muted">倍率</dt><dd class="tabular">{{ n.rate }}</dd>
              <dt class="text-muted">在线用户</dt><dd class="tabular">{{ fmt.num(n.online_users) }}</dd>
              <dt class="text-muted">今日流量</dt><dd class="tabular">{{ fmt.bytes(n.today_bytes) }}</dd>
              <dt class="text-muted">最近心跳</dt><dd>{{ fmt.ago(n.last_heartbeat_at) }}</dd>
              <dt class="text-muted">最近上报</dt><dd>{{ fmt.ago(n.last_push_at) }}</dd>
              <dt class="text-muted">下发到订阅</dt><dd>{{ n.in_subscription ? '是' : '否（待接入）' }}</dd>
              <dt class="text-muted">月成本</dt><dd class="tabular">{{ n.monthly_cost ? fmt.money(n.monthly_cost) : '未设置' }}</dd>
              <dt class="text-muted">版本</dt><dd class="tabular">v{{ n.version }}</dd>
            </Card>
          </Section>

          <Section title="入口" meta="订阅里每个启用的入口一条；不可用的入口暂不下发">
            <template #action><Button size="sm" @click="openEntryAdd">加入口</Button></template>
            <Card class="divide-y divide-line">
              <div v-for="e in entries.data.value?.items ?? []" :key="e.id || 'implicit'" class="flex flex-col gap-1.5 p-4">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-medium">{{ e.name }}</span>
                  <Badge>{{ entryKinds[e.kind] }}</Badge>
                  <RouterLink v-if="e.route" :to="{ name: 'routes' }" class="text-[13px]">{{ e.route.name }}</RouterLink>
                  <Badge :tone="entryTone(e)">{{ !e.enabled ? '停用' : e.available ? '可用' : '不可用' }}</Badge>
                  <span class="ml-auto font-mono text-[13px]">{{ e.host }}:{{ e.port }}</span>
                </div>
                <p v-if="e.enabled && !e.available" class="text-[13px] text-bad">{{ e.reason }}</p>
                <p v-if="e.implicit" class="text-[13px] text-muted">节点还没有登记入口，订阅里用节点自己的地址。</p>
                <ul v-if="e.forwards.length" class="font-mono text-[13px] text-muted">
                  <li v-for="f in e.forwards" :key="f.id">
                    {{ f.hop + 1 }}. {{ f.machine }}:{{ f.listen_port }}<template v-if="f.public_port !== f.listen_port">（外网 {{ f.public_port }}）</template>
                    → {{ f.target_host }}:{{ f.target_port }}
                    <Badge :tone="forwardStatus[f.status].tone">{{ forwardStatus[f.status].label }}</Badge>
                  </li>
                </ul>
                <div class="flex flex-wrap items-center gap-3 text-[13px]">
                  <span class="text-muted tabular">今日 {{ fmt.bytes(e.today_up + e.today_down) }}</span>
                  <template v-if="!e.implicit">
                    <button v-if="e.enabled" type="button" class="text-accent hover:text-accent-strong" @click="entryAction(e, 'disable')">停用</button>
                    <button v-else type="button" class="text-accent hover:text-accent-strong" @click="entryAction(e, 'enable')">启用</button>
                    <button type="button" class="text-bad" @click="entryAction(e, 'delete')">删除</button>
                  </template>
                </div>
              </div>
            </Card>
          </Section>

          <Section title="所属套餐">
            <template #action><Button size="sm" :disabled="unattached.length === 0" @click="openAttach">挂到套餐</Button></template>
            <Card class="flex flex-wrap gap-2 p-4">
              <span v-for="p in n.plans" :key="p.id" class="inline-flex items-center gap-1">
                <Badge tone="accent">{{ p.name }}</Badge>
                <button type="button" class="rounded px-1 text-[13px] text-muted hover:bg-bg" :aria-label="`从${p.name}卸载`" @click="detach(p.code, p.name)">卸载</button>
              </span>
              <span v-if="n.plans.length === 0" class="text-muted">没有挂到任何套餐，用户看不到这个节点。</span>
            </Card>
          </Section>

          <Section title="协议设置" meta="私钥和密码已隐去">
            <Card as="dl" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 p-5 text-[13px]">
              <template v-for="[k, v] in settings" :key="k">
                <dt class="font-mono text-muted">{{ k }}</dt>
                <dd class="font-mono break-all">{{ show(v) }}</dd>
              </template>
            </Card>
          </Section>
        </div>

        <div class="flex min-w-0 flex-[1_1_300px] flex-col gap-6">
          <Card v-if="n.runtime" as="section" aria-labelledby="rt-h" class="flex flex-col gap-3 p-5">
            <div class="flex items-center justify-between">
              <h2 id="rt-h" class="text-[15px] font-semibold">patato-node 运行状态</h2>
              <Badge :tone="n.runtime.running ? 'ok' : 'bad'">{{ n.runtime.running ? '运行中' : '未运行' }}</Badge>
            </div>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13px]">
              <dt class="text-muted">用户</dt><dd class="tabular">{{ fmt.num(n.runtime.users) }}</dd>
              <dt class="text-muted">连接</dt><dd class="tabular">{{ fmt.num(n.runtime.connections) }}</dd>
              <template v-if="n.runtime.listening !== undefined"><dt class="text-muted">端口监听</dt><dd>{{ n.runtime.listening ? '正常' : '没有监听' }}</dd></template>
              <template v-if="n.runtime.cert_expires_at"><dt class="text-muted">证书到期</dt><dd>{{ fmt.date(n.runtime.cert_expires_at) }}</dd></template>
              <dt class="text-muted">最近应用</dt><dd>{{ fmt.dateTime(n.runtime.applied_at) }}</dd>
            </dl>
          </Card>
          <CliHint v-if="n.backend === 'uniproxy'" title="安装或更新节点后端" :command="`patato node install-cmd ${n.code}`">
            安装命令里含节点密钥，只在终端里取。拿到后用 tolato exec 在机器上原样执行：
          </CliHint>
          <Card as="section" aria-labelledby="ops-h" class="flex flex-col gap-3 p-5">
            <h2 id="ops-h" class="text-[15px] font-semibold">手动操作</h2>
            <p class="text-[13px] text-muted">和 patato CLI 一样先预演再执行，敏感操作转审批。</p>
            <div class="flex flex-wrap gap-2">
              <Button size="sm" @click="openEdit">修改</Button>
              <Button size="sm" @click="rotate">轮换密钥</Button>
              <Button size="sm" variant="danger" @click="remove">删除</Button>
            </div>
          </Card>
          <Alert v-if="n.allow_legacy_token" tone="warn">这个节点还接受 Xboard 的全局 token。机器换成节点密钥后，迁移过渡期结束时应关掉。</Alert>
        </div>
      </div>
      <ChangeDialog v-if="op" v-model:open="opOpen" :title="op.title" :description="op.description" :request="op.request" :confirm-only="op.confirm" @done="done">
        <template v-if="op.title === '修改节点'">
          <Field v-model="edit.name" label="名称" />
          <Field v-model="edit.address" label="连接地址" />
          <div class="grid grid-cols-3 gap-3">
            <Field v-model="edit.port" label="对外端口" inputmode="numeric" hint="客户端连接的端口" />
            <Field v-model="edit.serverPort" label="监听端口" inputmode="numeric" hint="节点实际监听" />
            <Field v-model="edit.rate" label="流量倍率" inputmode="numeric" />
          </div>
          <Field v-model="edit.tags" label="标签" hint="逗号分隔；带 standby 标签的节点可被自动处置规则用来替换掉线节点" />
          <Field v-model="edit.cost" label="月成本（元）" hint="只用于成本报表" inputmode="numeric" />
        </template>
        <template v-else-if="op.title === '加入口'">
          <label class="flex flex-col gap-1.5">
            <span class="font-medium">线路</span>
            <select v-model="entryForm.route" class="field-input">
              <option value="">不经过中转（自定义地址）</option>
              <option v-for="r in routes.data.value?.items ?? []" :key="r.id" :value="r.code">{{ r.name }}：{{ r.hops.map((h) => h.host).join(' → ') }}</option>
            </select>
          </label>
          <Field v-model="entryForm.suffix" label="名称后缀" hint="订阅里为「节点名 · 后缀」，默认线路名" />
          <div class="grid grid-cols-2 gap-3">
            <Field v-model="entryForm.address" label="入口地址" hint="中转入口可留空，用第一跳机器的对外地址" />
            <Field v-model="entryForm.port" label="入口端口" inputmode="numeric" hint="中转入口留空，由服务端分配" />
          </div>
        </template>
        <label v-else-if="op.title === '挂到套餐'" class="flex flex-col gap-1.5">
          <span class="font-medium">套餐</span>
          <select v-model="attachPlan" class="field-input"><option v-for="p in unattached" :key="p.id" :value="p.code">{{ p.name }}（{{ fmt.num(p.user_count) }} 名用户）</option></select>
        </label>
      </ChangeDialog>
    </template>
  </LoadState>
</template>
