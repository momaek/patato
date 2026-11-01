<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type Node, type Plan } from '@patato/api'
import { Alert, Badge, Button, Card, Field, LoadState, Section, StatusDot } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
import { cents, type ChangeRequest } from '../lib/change'
import { backends, protocols, show } from '../lib/labels'

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

// 手动操作都走变更计划：预演 → 执行，敏感时转审批
const op = ref<{ title: string; description?: string; confirm?: boolean; request: () => ChangeRequest }>()
const opOpen = ref(false)
const edit = ref({ name: '', address: '', port: '', rate: '', tags: '', cost: '' })
const attachPlan = ref('')
function run(o: NonNullable<typeof op.value>) {
  op.value = o
  opOpen.value = true
}
function openEdit() {
  const x = n.value!
  edit.value = { name: x.name, address: x.address, port: String(x.port), rate: String(x.rate), tags: x.tags.join(', '), cost: x.monthly_cost ? String(x.monthly_cost / 100) : '' }
  run({
    title: '修改节点',
    request: () => {
      const e = edit.value
      const body: Record<string, unknown> = {}
      if (e.name.trim() !== x.name) body.name = e.name.trim()
      if (e.address.trim() !== x.address) body.address = e.address.trim()
      if (Number(e.port) !== x.port) body.port = Number(e.port)
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
          <div class="grid grid-cols-2 gap-3">
            <Field v-model="edit.port" label="端口" inputmode="numeric" />
            <Field v-model="edit.rate" label="流量倍率" inputmode="numeric" />
          </div>
          <Field v-model="edit.tags" label="标签" hint="逗号分隔；带 standby 标签的节点可被自动处置规则用来替换掉线节点" />
          <Field v-model="edit.cost" label="月成本（元）" hint="只用于成本报表" inputmode="numeric" />
        </template>
        <label v-else-if="op.title === '挂到套餐'" class="flex flex-col gap-1.5">
          <span class="font-medium">套餐</span>
          <select v-model="attachPlan" class="field-input"><option v-for="p in unattached" :key="p.id" :value="p.code">{{ p.name }}（{{ fmt.num(p.user_count) }} 名用户）</option></select>
        </label>
      </ChangeDialog>
    </template>
  </LoadState>
</template>
