<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { api, errorText, fmt, type ApprovalDetail } from '@patato/api'
import { Alert, Badge, Button, Card, Code, Dialog, LoadState, Section } from '@patato/ui'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import { approve, passkeyError, supported } from '../lib/passkey'
import { approvalStates, entries, fields, risks, show, subjectKinds } from '../lib/labels'

const route = useRoute()
const qc = useQueryClient()
const id = computed(() => String(route.params.id))
// CLI 运行 patato approval approve <id> 时打开 ?via=cli，审计里记为「CLI 拉起的 passkey」
const fromCLI = computed(() => route.query.via === 'cli')
const q = useQuery({
  queryKey: ['approval', id],
  queryFn: () => api.get<ApprovalDetail>('/api/v1/approvals/{id}', { params: { id: id.value } }),
  refetchInterval: (query) => (query.state.data?.status === 'pending' ? 10_000 : false),
})
const busy = ref<'approve' | 'reject' | ''>('')
const error = ref('')
const rejectOpen = ref(false)
const reason = ref('')

const a = computed(() => q.data.value)
const expired = computed(() => !!a.value && a.value.status === 'pending' && new Date(a.value.expires_at) < new Date())
const status = computed(() => (expired.value ? 'expired' : a.value?.status ?? 'pending'))
const escalated = computed(() => a.value && a.value.change.declared_risk !== a.value.change.risk)

async function doApprove() {
  error.value = ''
  busy.value = 'approve'
  try {
    const d = await approve(id.value, fromCLI.value ? 'cli_passkey' : 'web')
    qc.setQueryData(['approval', id], d)
    qc.invalidateQueries({ queryKey: ['approvals'] })
    qc.invalidateQueries({ queryKey: ['status'] })
    toast.success(d.change.status === 'applied' ? '已批准并执行' : '已批准')
  } catch (e) {
    error.value = passkeyError(e) ?? errorText(e)
    q.refetch()
  } finally {
    busy.value = ''
  }
}

async function doReject() {
  error.value = ''
  busy.value = 'reject'
  try {
    const d = await api.post<ApprovalDetail>('/api/v1/approvals/{id}:reject', { params: { id: id.value }, body: { reason: reason.value.trim() } })
    qc.setQueryData(['approval', id], d)
    qc.invalidateQueries({ queryKey: ['approvals'] })
    rejectOpen.value = false
    toast('已拒绝')
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = ''
  }
}

function targetText(t: { type: string; id: number; version: number }) {
  return `${{ plan: '套餐', node: '节点', user: '用户', template: '模板', machine: '机器' }[t.type] ?? t.type} #${t.id}，版本 ${t.version}`
}
</script>

<template>
  <LoadState :loading="q.isPending.value" :error="q.error.value" :message="q.error.value ? errorText(q.error.value) : undefined" @retry="q.refetch()">
    <template v-if="a">
      <PageHeader :title="a.change.summary">
        <template #crumbs>
          <p class="text-[13px] text-muted">
            <RouterLink :to="{ name: 'approvals' }">审批</RouterLink> / <span class="font-mono">{{ a.approval_id }}</span>
          </p>
          <div class="flex flex-wrap items-center gap-2">
            <Badge :tone="risks[a.change.risk]?.tone">{{ risks[a.change.risk]?.label }}</Badge>
            <span v-if="escalated" class="text-[13px] text-muted">接口声明为{{ risks[a.change.declared_risk]?.label }}，因影响面升级</span>
            <Badge v-if="status !== 'pending'" :tone="approvalStates[status]?.tone">{{ approvalStates[status]?.label }}</Badge>
          </div>
        </template>
      </PageHeader>

      <div class="flex flex-wrap items-start gap-7">
        <div class="flex min-w-0 flex-[2_1_520px] flex-col gap-8">
          <Section title="影响范围">
            <Card as="dl" class="grid gap-4 p-5 sm:grid-cols-[repeat(2,minmax(0,1fr))_2fr]">
              <div><dt class="text-muted">受影响用户</dt><dd class="text-2xl font-semibold tabular">{{ fmt.num(a.change.impact.users) }}</dd></div>
              <div><dt class="text-muted">涉及节点</dt><dd class="text-2xl font-semibold tabular">{{ fmt.num(a.change.impact.nodes) }}</dd></div>
              <div v-if="a.change.risk_reason"><dt class="text-muted">升级原因</dt><dd>{{ a.change.risk_reason }}</dd></div>
            </Card>
          </Section>

          <Section title="变更内容" meta="来自预演结果，批准后按这份计划执行">
            <Table :min-width="560" label="变更内容">
              <thead><tr><th scope="col">字段</th><th scope="col">现在</th><th scope="col">批准后</th></tr></thead>
              <tbody>
                <tr v-for="d in a.change.diff ?? []" :key="d.field">
                  <th scope="row" class="w-36 !border-b-0 !text-[14px] !font-medium !text-ink">{{ d.field }}</th>
                  <td v-for="(v, i) in [d.before, d.after]" :key="i" :class="i === 0 ? 'text-muted' : ''">
                    <dl v-if="fields(v)" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
                      <template v-for="[k, x] in fields(v)" :key="k"><dt class="text-muted">{{ k }}</dt><dd>{{ x }}</dd></template>
                    </dl>
                    <template v-else>{{ show(v) }}</template>
                  </td>
                </tr>
                <tr v-if="a.change.targets?.length">
                  <th scope="row" class="w-36 !border-b-0 !text-[14px] !font-medium !text-ink">版本校验</th>
                  <td class="text-muted"><span v-for="t in a.change.targets" :key="t.type + t.id" class="block">{{ targetText(t) }}</span></td>
                  <td>执行时版本必须仍一致，否则作废</td>
                </tr>
              </tbody>
            </Table>
          </Section>

          <Section title="执行步骤">
            <Card as="ol" class="flex list-decimal flex-col gap-2 py-4 pr-5 pl-10">
              <li v-for="(s, i) in a.change.plan" :key="i">{{ s }}</li>
            </Card>
          </Section>

          <Section v-if="a.change.result || a.change.error" title="执行结果">
            <Alert v-if="a.change.error">{{ a.change.error }}</Alert>
            <Code v-else :text="JSON.stringify(a.change.result, null, 2)" block />
          </Section>
        </div>

        <aside aria-label="审批信息" class="flex min-w-0 flex-[1_1_300px] flex-col gap-6 lg:sticky lg:top-8">
          <Card as="section" aria-labelledby="decide-h" class="flex flex-col gap-3 p-5">
            <div class="flex flex-col gap-0.5">
              <h2 id="decide-h" class="text-lg font-semibold">{{ status === 'pending' ? '你的决定' : '结果' }}</h2>
              <p v-if="status === 'pending'" class="text-[13px] text-muted">{{ fmt.until(a.expires_at) }}过期（{{ fmt.time(a.expires_at) }}）</p>
            </div>
            <template v-if="status === 'pending'">
              <Alert v-if="fromCLI" tone="accent">这是 patato CLI 拉起的确认页。批准后回到终端，CLI 会自动继续。</Alert>
              <Alert v-if="!supported()" tone="warn">这个浏览器不支持 passkey，用 Telegram 卡片批准。</Alert>
              <Alert v-if="error">{{ error }}</Alert>
              <Button variant="primary" size="lg" block :loading="busy === 'approve'" :disabled="!!busy || !supported()" @click="doApprove">用 passkey 批准</Button>
              <Button size="lg" block :disabled="!!busy" @click="rejectOpen = true">拒绝</Button>
              <p class="text-[13px] text-muted">这次批准只对这一个变更有效。执行时如果相关对象已被改过，批准会作废，需要重新发起。</p>
              <div class="flex flex-col gap-1.5 border-t border-line-soft pt-3 text-[13px] text-muted">
                <p>不在电脑前？Telegram 里的审批卡片同样可以批准。在终端里也可以运行：</p>
                <Code :text="`patato approval approve ${a.approval_id}`" block />
              </div>
            </template>
            <template v-else>
              <p>
                {{ approvalStates[status]?.label }}<template v-if="a.decided_by">，由 {{ a.decided_by }} 经{{ entries[a.decided_via ?? ''] ?? a.decided_via }}操作</template>
                <template v-if="a.decided_at">（{{ fmt.dateTime(a.decided_at) }}）</template>
              </p>
              <p v-if="a.reason" class="text-[13px] text-muted">原因：{{ a.reason }}</p>
              <p v-if="status === 'expired'" class="text-[13px] text-muted">审批已过期，变更没有执行。需要的话让发起方重新预演。</p>
            </template>
          </Card>

          <Card as="section" aria-labelledby="who-h" class="flex flex-col gap-3 p-5">
            <h2 id="who-h" class="text-lg font-semibold">发起方</h2>
            <figure v-if="a.instruction" class="flex flex-col gap-1">
              <figcaption class="text-[13px] text-muted">原始指令</figcaption>
              <blockquote class="border-l-[3px] border-accent pl-3 text-[15px]">{{ a.instruction }}</blockquote>
            </figure>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13px]">
              <dt class="text-muted">凭证</dt><dd>{{ subjectKinds[a.requester.kind] ?? a.requester.kind }}，{{ a.requester.label }}</dd>
              <template v-if="a.requester.actor_claim"><dt class="text-muted">调用方自报</dt><dd>{{ a.requester.actor_claim }}</dd></template>
              <dt class="text-muted">入口</dt><dd>{{ entries[a.requester.entry] ?? a.requester.entry }}</dd>
              <dt class="text-muted">变更计划</dt><dd class="font-mono">{{ a.change.change_id }}</dd>
              <dt class="text-muted">发起时间</dt><dd class="tabular">{{ fmt.date(a.created_at) }} {{ fmt.time(a.created_at, true) }}</dd>
            </dl>
          </Card>
        </aside>
      </div>

      <Dialog v-model:open="rejectOpen" title="拒绝这个变更？" description="变更不会执行。发起的会话会收到拒绝结果和你写的原因。">
        <form class="flex flex-col gap-3" @submit.prevent="doReject">
          <label class="flex flex-col gap-1.5">
            <span class="font-medium">原因（可选）</span>
            <textarea v-model="reason" rows="3" maxlength="500" class="rounded-lg border border-control px-3 py-2 outline-none focus:border-accent focus:ring-2 focus:ring-accent/20" />
          </label>
          <Alert v-if="error">{{ error }}</Alert>
          <div class="flex justify-end gap-2">
            <Button @click="rejectOpen = false">取消</Button>
            <Button type="submit" variant="danger" :loading="busy === 'reject'">拒绝</Button>
          </div>
        </form>
      </Dialog>
    </template>
  </LoadState>
</template>
