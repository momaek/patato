<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type Node } from '@patato/api'
import { Alert, Badge, Card, LoadState, Section, StatusDot } from '@patato/ui'
import CliHint from '../components/CliHint.vue'
import PageHeader from '../components/PageHeader.vue'
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
              <dt class="text-muted">版本</dt><dd class="tabular">v{{ n.version }}</dd>
            </Card>
          </Section>

          <Section title="所属套餐">
            <Card class="flex flex-wrap gap-2 p-4">
              <Badge v-for="p in n.plans" :key="p.id" tone="accent">{{ p.name }}</Badge>
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
          <CliHint title="改设置或挂载" :command="`patato node update ${n.code} --dry-run`">修改都走 Agent 或 patato CLI，先预演再执行：</CliHint>
          <Alert v-if="n.allow_legacy_token" tone="warn">这个节点还接受 Xboard 的全局 token。机器换成节点密钥后，迁移过渡期结束时应关掉。</Alert>
        </div>
      </div>
    </template>
  </LoadState>
</template>
