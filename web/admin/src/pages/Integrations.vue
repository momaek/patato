<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, type AutomationRule, type List, type Webhook } from '@patato/api'
import { Badge, Button, Code, Dialog, Empty, Field, LoadState, Section } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'
import { automationActions, eventKinds } from '../lib/labels'

const hooks = useQuery({ queryKey: ['webhooks'], queryFn: () => api.get<List<Webhook>>('/api/v1/webhooks') })
const rules = useQuery({ queryKey: ['automations'], queryFn: () => api.get<List<AutomationRule>>('/api/v1/automations') })
const kindNames = (ks: string[]) => (ks.length ? ks.map((k) => eventKinds[k] ?? k).join('、') : '全部事件')

// Webhook
const hookOpen = ref(false)
const hook = ref({ name: '', url: '', kinds: [] as string[] })
function openHook() {
  hook.value = { name: '', url: '', kinds: [] }
  hookOpen.value = true
}
const hookRequest = (): ChangeRequest => ({ method: 'post', path: '/api/v1/webhooks', body: { name: hook.value.name.trim(), url: hook.value.url.trim(), kinds: hook.value.kinds } })
const deleting = ref<{ title: string; req: ChangeRequest }>()
const deleteOpen = ref(false)
function del(title: string, req: ChangeRequest) {
  deleting.value = { title, req }
  deleteOpen.value = true
}
const secret = ref('')
const secretOpen = ref(false)
async function showSecret(w: Webhook) {
  try {
    secret.value = (await api.get<{ secret: string }>('/api/v1/webhooks/{id}/secret', { params: { id: w.id } })).secret
    secretOpen.value = true
  } catch (e) {
    toast.error(errorText(e))
  }
}

// 自动处置规则
const examples: Record<string, string> = {
  email_user: JSON.stringify({ subject: '{{service}} 订阅即将到期', body: '{{email}}，{{message}}。续费：{{portal_url}}/buy' }, null, 2),
  operation: JSON.stringify({ kind: 'user.reset_token', params: { user: '{{subject_id}}' } }, null, 2),
  replace_offline_node: JSON.stringify({ standby_tag: 'standby' }, null, 2),
}
const ruleOpen = ref(false)
const rule = ref({ name: '', event: 'user_expiring', action: 'email_user', params: examples.email_user })
function openRule() {
  rule.value = { name: '', event: 'user_expiring', action: 'email_user', params: examples.email_user }
  ruleOpen.value = true
}
function ruleRequest(): ChangeRequest {
  let params: unknown
  try {
    params = JSON.parse(rule.value.params || '{}')
  } catch {
    throw new Error('动作参数不是合法 JSON')
  }
  return { method: 'post', path: '/api/v1/automations', body: { name: rule.value.name.trim(), event: rule.value.event, action: rule.value.action, params } }
}
const toggling = ref<AutomationRule>()
const toggleOpen = ref(false)
function toggle(r: AutomationRule) {
  toggling.value = r
  toggleOpen.value = true
}
const toggleRequest = (): ChangeRequest => ({ method: 'patch', path: '/api/v1/automations/{id}', params: { id: toggling.value!.id }, body: { enabled: !toggling.value!.enabled } })
const eventList = computed(() => Object.entries(eventKinds))
const placeholderHelp = '邮件可用 {{email}} {{message}} {{service}} {{portal_url}}；变更参数可用 {{subject_id}} 和 {{payload.字段}}。'
</script>

<template>
  <PageHeader title="集成" lead="事件推送到外部系统（例如常驻的 Agent 运行器），以及系统自动执行的处置规则。两者都会长期生效，新建需要审批。" />

  <Section title="Webhook" meta="每次推送带 X-Patato-Signature: sha256=HMAC(密钥, 时间戳.请求体)">
    <template #action><Button size="sm" @click="openHook">新建 Webhook</Button></template>
    <LoadState :loading="hooks.isPending.value" :error="hooks.error.value" @retry="hooks.refetch()">
      <Table :min-width="640" label="Webhook">
        <thead><tr><th>名称</th><th>地址</th><th>事件</th><th>最近推送</th><th /></tr></thead>
        <tbody>
          <tr v-for="w in hooks.data.value?.items ?? []" :key="w.id">
            <td class="font-medium">{{ w.name }}</td>
            <td class="font-mono text-[13px] break-all">{{ w.url }}</td>
            <td class="text-[13px]">{{ kindNames(w.kinds) }}</td>
            <td class="text-[13px] text-muted">{{ w.last_status || '—' }}<template v-if="w.last_delivery_at">，{{ fmt.ago(w.last_delivery_at) }}</template></td>
            <td class="text-right whitespace-nowrap">
              <Button size="sm" variant="ghost" @click="showSecret(w)">签名密钥</Button>
              <Button size="sm" variant="ghost" @click="del(`删除 Webhook ${w.name}`, { method: 'delete', path: '/api/v1/webhooks/{id}', params: { id: w.id } })">删除</Button>
            </td>
          </tr>
          <tr v-if="(hooks.data.value?.items ?? []).length === 0"><td colspan="5"><Empty title="没有 Webhook" /></td></tr>
        </tbody>
      </Table>
    </LoadState>
  </Section>

  <Section title="自动处置规则" meta="以系统身份走变更计划：动作被判为敏感时只生成审批，由人批准">
    <template #action><Button size="sm" @click="openRule">新建规则</Button></template>
    <LoadState :loading="rules.isPending.value" :error="rules.error.value" @retry="rules.refetch()">
      <Table :min-width="720" label="自动处置规则">
        <thead><tr><th>规则</th><th>事件</th><th>动作</th><th>状态</th><th>最近结果</th><th /></tr></thead>
        <tbody>
          <tr v-for="r in rules.data.value?.items ?? []" :key="r.id">
            <td class="font-medium">{{ r.name }}</td>
            <td>{{ eventKinds[r.event] ?? r.event }}</td>
            <td>{{ automationActions[r.action] ?? r.action }}</td>
            <td><Badge :tone="r.enabled ? 'ok' : 'neutral'">{{ r.enabled ? '启用' : '停用' }}</Badge></td>
            <td class="text-[13px] text-muted">{{ r.last_result || '还没触发过' }}<template v-if="r.last_fired_at">（{{ fmt.ago(r.last_fired_at) }}）</template></td>
            <td class="text-right whitespace-nowrap">
              <Button size="sm" variant="ghost" @click="toggle(r)">{{ r.enabled ? '停用' : '启用' }}</Button>
              <Button size="sm" variant="ghost" @click="del(`删除规则 ${r.name}`, { method: 'delete', path: '/api/v1/automations/{id}', params: { id: r.id } })">删除</Button>
            </td>
          </tr>
          <tr v-if="(rules.data.value?.items ?? []).length === 0"><td colspan="6"><Empty title="没有规则">例如：用户即将到期时发提醒邮件；节点掉线时用同地区的备用节点顶上。</Empty></td></tr>
        </tbody>
      </Table>
    </LoadState>
  </Section>

  <ChangeDialog v-model:open="hookOpen" title="新建 Webhook" description="事件内容含用户邮箱等数据，外发数据需要审批。" :request="hookRequest">
    <Field v-model="hook.name" label="名称" required />
    <Field v-model="hook.url" label="接收地址" placeholder="https://agent.example.com/patato" required />
    <fieldset class="flex flex-col gap-1.5">
      <legend class="mb-1.5 font-medium">事件（不选为全部）</legend>
      <div class="grid grid-cols-2 gap-x-4 gap-y-1">
        <label v-for="[k, v] in eventList" :key="k" class="flex items-center gap-1.5 text-[13px]"><input v-model="hook.kinds" type="checkbox" :value="k" class="accent-accent" />{{ v }}</label>
      </div>
    </fieldset>
  </ChangeDialog>

  <ChangeDialog v-model:open="ruleOpen" title="新建自动处置规则" :request="ruleRequest">
    <Field v-model="rule.name" label="名称" required />
    <label class="flex flex-col gap-1.5">
      <span class="font-medium">事件</span>
      <select v-model="rule.event" class="field-input"><option v-for="[k, v] in eventList" :key="k" :value="k">{{ v }}</option></select>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="font-medium">动作</span>
      <select v-model="rule.action" class="field-input" @change="rule.params = examples[rule.action]">
        <option v-for="(v, k) in automationActions" :key="k" :value="k">{{ v }}</option>
      </select>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="font-medium">动作参数（JSON）</span>
      <textarea v-model="rule.params" rows="6" class="field-input py-2 font-mono text-[13px]" />
      <span class="text-[13px] text-muted">{{ placeholderHelp }}</span>
    </label>
  </ChangeDialog>

  <ChangeDialog v-if="toggling" v-model:open="toggleOpen" :title="`${toggling.enabled ? '停用' : '启用'}规则 ${toggling.name}`" :request="toggleRequest" confirm-only />
  <ChangeDialog v-if="deleting" v-model:open="deleteOpen" :title="deleting.title" :request="() => deleting!.req" confirm-only />

  <Dialog v-model:open="secretOpen" title="签名密钥" description="拿到它就能伪造推送，只配置在接收方，不要发给别人。">
    <Code :text="secret" block />
  </Dialog>
</template>
