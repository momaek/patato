<script setup lang="ts">
// 手动操作的对话框：填表 → 预演（看计划、影响面、风险）→ 执行。敏感操作执行后转到审批页。
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useQueryClient } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { errorText, fmt, type ChangePlan } from '@patato/api'
import { Alert, Badge, Button, Dialog } from '@patato/ui'
import { apply, preview, type ChangeRequest } from '../lib/change'
import { fieldName, risks, show } from '../lib/labels'

const props = defineProps<{
  title: string
  description?: string
  /** 生成请求；表单不合法时抛出错误，错误文字直接显示。 */
  request: () => ChangeRequest
  /** 跳过表单，打开就预演（只需要确认的操作）。 */
  confirmOnly?: boolean
}>()
const emit = defineEmits<{ done: [plan: ChangePlan] }>()
const open = defineModel<boolean>('open', { default: false })
const router = useRouter()
const qc = useQueryClient()

const stage = ref<'form' | 'preview'>('form')
const plan = ref<ChangePlan>()
const busy = ref(false)
const error = ref('')

watch(open, (v) => {
  if (!v) return
  stage.value = 'form'
  plan.value = undefined
  error.value = ''
  if (props.confirmOnly) doPreview()
})

async function doPreview() {
  error.value = ''
  busy.value = true
  try {
    plan.value = await preview(props.request())
    stage.value = 'preview'
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}

async function doApply() {
  if (!plan.value) return
  error.value = ''
  busy.value = true
  try {
    const p = await apply(plan.value.change_id)
    qc.invalidateQueries()
    open.value = false
    emit('done', p)
    if (p.status === 'pending_approval' && p.approval_id) {
      toast('已发起审批，批准后执行')
      router.push({ name: 'approval', params: { id: p.approval_id } })
    } else if (p.status === 'applied') {
      toast.success('已执行：' + p.summary)
    } else {
      toast.error(`变更${p.status === 'stale' ? '已作废' : '没有执行'}：${p.error ?? p.status}`)
    }
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open" :title="title" :description="stage === 'form' ? description : undefined">
    <form v-if="stage === 'form'" class="flex flex-col gap-4" @submit.prevent="doPreview">
      <slot />
      <Alert v-if="error">{{ error }}</Alert>
      <div class="flex justify-end gap-2">
        <Button @click="open = false">取消</Button>
        <Button type="submit" variant="primary" :loading="busy">预演</Button>
      </div>
    </form>
    <div v-else-if="plan" class="flex flex-col gap-4">
      <div class="flex flex-col gap-1.5">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-semibold">{{ plan.summary }}</span>
          <Badge :tone="risks[plan.risk]?.tone">{{ risks[plan.risk]?.label }}</Badge>
        </div>
        <p class="text-[13px] text-muted">影响 {{ fmt.num(plan.impact.users) }} 名用户、{{ fmt.num(plan.impact.nodes) }} 个节点<template v-if="plan.risk_reason">。{{ plan.risk_reason }}</template></p>
      </div>
      <ol class="flex list-decimal flex-col gap-1 pl-5">
        <li v-for="(s, i) in plan.plan" :key="i">{{ s }}</li>
      </ol>
      <dl v-if="plan.diff?.length" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-lg bg-bg px-3 py-2 text-[13px]">
        <template v-for="d in plan.diff" :key="d.field">
          <dt class="text-muted">{{ fieldName(d.field) }}</dt>
          <dd class="break-all">{{ show(d.before) }} → {{ show(d.after) }}</dd>
        </template>
      </dl>
      <Alert v-if="plan.risk === 'sensitive'" tone="warn">这是敏感操作：执行后生成审批，需要用 passkey 批准后才生效。</Alert>
      <Alert v-if="error">{{ error }}</Alert>
      <div class="flex justify-end gap-2">
        <Button v-if="!confirmOnly" @click="stage = 'form'">返回修改</Button>
        <Button v-else @click="open = false">取消</Button>
        <Button variant="primary" :loading="busy" @click="doApply">{{ plan.risk === 'sensitive' ? '发起审批' : '执行' }}</Button>
      </div>
    </div>
    <div v-else class="flex flex-col gap-3">
      <Alert v-if="error">{{ error }}</Alert>
      <p v-else class="text-muted">正在预演…</p>
      <div class="flex justify-end"><Button @click="open = false">关闭</Button></div>
    </div>
  </Dialog>
</template>
