<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, errorText, fmt, type List, type RulePreset, type RuleProfile } from '@patato/api'
import { Alert, Badge, Button, Card, Empty, Field, LoadState, Section } from '@patato/ui'
import ChangeDialog from '../components/ChangeDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import Table from '../components/Table.vue'
import type { ChangeRequest } from '../lib/change'

const route = useRoute()
const router = useRouter()
const list = useQuery({ queryKey: ['rule-profiles'], queryFn: () => api.get<List<RuleProfile>>('/api/v1/rule-profiles') })
const profiles = computed(() => list.data.value?.items ?? [])
const presets = useQuery({ queryKey: ['rule-profiles', 'presets'], queryFn: () => api.get<List<RulePreset>>('/api/v1/rule-profiles/presets') })
const selectedCode = computed(() => (typeof route.query.profile === 'string' ? route.query.profile : profiles.value[0]?.code))
const detail = useQuery({
  queryKey: ['rule-profile', selectedCode],
  queryFn: () => api.get<RuleProfile>('/api/v1/rule-profiles/{id}', { params: { id: selectedCode.value! } }),
  enabled: computed(() => !!selectedCode.value),
})
const imported = computed(() => new Set(profiles.value.map((p) => p.source_url)))

function source(p: RuleProfile) {
  if (!p.source_url) return '粘贴的内容'
  try {
    const u = new URL(p.source_url)
    return u.pathname.split('/').pop() || u.host
  } catch {
    return p.source_url
  }
}

// 新建：从 ACL4SSR 预设导入、填 ini 地址或直接粘贴内容
type Mode = 'preset' | 'url' | 'content'
const form = ref({ mode: 'preset' as Mode, preset: '', code: '', name: '', description: '', url: '', content: '', selectable: true })
const creating = ref(false)
const formOpen = ref(false)
function openCreate(preset?: RulePreset) {
  creating.value = true
  form.value = { mode: 'preset', preset: preset?.code ?? presets.data.value?.items[0]?.code ?? '', code: '', name: '', description: '', url: '', content: '', selectable: true }
  formOpen.value = true
}
function openEdit(p: RuleProfile) {
  creating.value = false
  form.value = { mode: p.source_url ? 'url' : 'content', preset: '', code: p.code, name: p.name, description: p.description, url: p.source_url,
    content: p.source_url ? '' : (p.content ?? ''), selectable: p.user_selectable }
  formOpen.value = true
}
function formRequest(): ChangeRequest {
  const f = form.value
  const body: Record<string, unknown> = { user_selectable: f.selectable }
  if (f.name.trim()) body.name = f.name.trim()
  if (creating.value || f.description !== detail.data.value?.description) body.description = f.description.trim()
  if (f.mode === 'preset') {
    if (!f.preset) throw new Error('选择一个 ACL4SSR 模板')
    body.preset = f.preset
  } else if (f.mode === 'url') {
    if (!f.url.trim()) throw new Error('填写 ini 模板地址')
    if (creating.value || f.url.trim() !== detail.data.value?.source_url) body.source_url = f.url.trim()
  } else {
    if (!f.content.trim()) throw new Error('粘贴 ini 模板内容')
    if (creating.value || f.content !== detail.data.value?.content || detail.data.value?.source_url) body.content = f.content
  }
  if (creating.value) {
    if (f.code.trim()) body.code = f.code.trim()
    return { method: 'post', path: '/api/v1/rule-profiles', body }
  }
  return { method: 'patch', path: '/api/v1/rule-profiles/{id}', params: { id: f.code }, body }
}
const presetPicked = computed(() => presets.data.value?.items.find((p) => p.code === form.value.preset))

const op = ref<{ title: string; request: () => ChangeRequest }>()
const opOpen = ref(false)
function run(title: string, request: () => ChangeRequest) {
  op.value = { title, request }
  opOpen.value = true
}
</script>

<template>
  <PageHeader title="分流规则" lead="决定订阅里有哪些策略组、哪些网站走哪个组。在套餐里设为默认规则，用户还可以在用户侧从允许自选的规则里换一个。">
    <Button variant="primary" @click="openCreate()">新建规则</Button>
  </PageHeader>
  <LoadState :loading="list.isPending.value" :error="list.error.value" :message="list.error.value ? errorText(list.error.value) : undefined" @retry="list.refetch()">
    <div class="flex flex-wrap items-start gap-7">
      <div class="flex min-w-0 flex-[3_1_560px] flex-col gap-7">
        <Table :min-width="560" label="分流规则列表">
          <thead>
            <tr><th>规则</th><th>来源</th><th>策略组</th><th>最近拉取</th><th class="!text-right">套餐</th><th class="!text-right">自选用户</th></tr>
          </thead>
          <tbody>
            <tr
              v-for="p in profiles"
              :key="p.id"
              :class="['cursor-pointer', p.code === selectedCode ? 'bg-accent-soft/60' : 'hover:bg-bg']"
              @click="router.replace({ query: { profile: p.code } })"
            >
              <td>
                <button type="button" class="text-left font-medium text-accent" :aria-pressed="p.code === selectedCode" @click.stop="router.replace({ query: { profile: p.code } })">{{ p.name }}</button>
                <p class="font-mono text-[13px] text-muted">{{ p.code }}</p>
              </td>
              <td class="text-[13px]">{{ source(p) }}</td>
              <td class="tabular">{{ p.groups.length }} 个</td>
              <td class="text-[13px]">
                <span v-if="p.fetch_error" class="font-medium text-bad">拉取失败</span>
                <span v-else-if="p.source_url">{{ p.fetched_at ? fmt.dateTime(p.fetched_at) : '—' }}</span>
                <span v-else class="text-muted">不需要</span>
              </td>
              <td class="text-right tabular">{{ p.plan_count }}</td>
              <td class="text-right tabular">{{ fmt.num(p.user_count) }}</td>
            </tr>
            <tr v-if="profiles.length === 0">
              <td colspan="6"><Empty title="还没有分流规则">没有设置时订阅用内置的简单规则（国内直连，其余走代理）。从下面导入一个 ACL4SSR 模板开始。</Empty></td>
            </tr>
          </tbody>
        </Table>

        <Section title="ACL4SSR 模板" meta="规则文件由 Patato 缓存后从订阅域名下发，用户不需要直连 GitHub">
          <ul class="grid grid-cols-1 gap-2.5 md:grid-cols-2">
            <li v-for="p in presets.data.value?.items ?? []" :key="p.code" class="flex flex-col gap-1.5 rounded-[10px] border border-line bg-card px-3.5 py-3">
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium">{{ p.name }}</span>
                <Badge v-if="imported.has(p.source_url)" tone="ok">已导入</Badge>
                <Button v-else size="sm" @click="openCreate(p)">导入</Button>
              </div>
              <span class="text-[13px] text-muted">{{ p.description }}</span>
            </li>
          </ul>
          <p class="text-[13px] text-muted">模板来自 <a href="https://github.com/ACL4SSR/ACL4SSR" target="_blank" rel="noopener">ACL4SSR</a>（CC-BY-SA-4.0）。也可以填任意 subconverter 格式的 ini 地址，或者直接粘贴内容。</p>
        </Section>
      </div>

      <Card v-if="detail.data.value" as="section" aria-labelledby="rp-h" class="flex min-w-0 flex-[2_1_320px] flex-col gap-5 p-5">
        <div class="flex flex-col gap-0.5">
          <h2 id="rp-h" class="text-xl font-semibold">{{ detail.data.value.name }}</h2>
          <p class="text-[13px] text-muted">版本 v{{ detail.data.value.version }}，{{ fmt.monthDay(detail.data.value.updated_at) }}修改</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <Button size="sm" @click="openEdit(detail.data.value)">修改</Button>
          <Button
            v-if="detail.data.value.source_url"
            size="sm"
            @click="run('立即拉取', () => ({ method: 'post', path: '/api/v1/rule-profiles/{id}:refresh', params: { id: detail.data.value!.code } }))"
          >立即拉取</Button>
          <Button size="sm" variant="danger" @click="run('删除分流规则', () => ({ method: 'delete', path: '/api/v1/rule-profiles/{id}', params: { id: detail.data.value!.code } }))">删除</Button>
        </div>
        <Alert v-if="detail.data.value.fetch_error" tone="warn">上次拉取失败：{{ detail.data.value.fetch_error }}。订阅继续使用之前拉到的内容。</Alert>
        <Alert v-if="detail.data.value.parse_error" tone="bad">模板解析失败：{{ detail.data.value.parse_error }}。使用它的用户暂时拿到内置规则。</Alert>
        <p v-if="detail.data.value.description" class="text-muted">{{ detail.data.value.description }}</p>
        <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
          <dt class="text-muted">来源</dt>
          <dd class="min-w-0 break-all">
            <a v-if="detail.data.value.source_url" :href="detail.data.value.source_url" target="_blank" rel="noopener">{{ detail.data.value.source_url }}</a>
            <span v-else>直接粘贴的内容</span>
          </dd>
          <template v-if="detail.data.value.source_url">
            <dt class="text-muted">最近拉取</dt><dd>{{ detail.data.value.fetched_at ? fmt.dateTime(detail.data.value.fetched_at) : '—' }}，每 6 小时自动更新</dd>
          </template>
          <dt class="text-muted">用户自选</dt><dd>{{ detail.data.value.user_selectable ? '允许' : '不允许' }}</dd>
          <dt class="text-muted">使用的用户</dt><dd class="tabular">{{ fmt.num(detail.data.value.user_count) }} 名</dd>
          <dt class="text-muted">规则</dt><dd class="tabular">{{ detail.data.value.rule_count }} 条，引用 {{ detail.data.value.list_count }} 个规则文件</dd>
        </dl>
        <Section title="策略组" :meta="`${detail.data.value.groups.length} 个`">
          <ul class="flex flex-wrap gap-1.5">
            <li v-for="g in detail.data.value.groups" :key="g" class="rounded-md bg-bg px-2 py-1 text-[13px]">{{ g }}</li>
          </ul>
        </Section>
        <Section v-if="detail.data.value.warnings?.length" title="解析时跳过的内容">
          <ul class="flex flex-col gap-1 text-[13px] text-muted">
            <li v-for="w in detail.data.value.warnings" :key="w">{{ w }}</li>
          </ul>
        </Section>
        <Section v-if="detail.data.value.lists?.length" title="规则文件" meta="客户端第一次请求时下载">
          <ul class="flex flex-col divide-y divide-line-soft text-[13px]">
            <li v-for="l in detail.data.value.lists" :key="l.url" class="flex items-center justify-between gap-3 py-1.5">
              <a :href="l.url" target="_blank" rel="noopener" class="min-w-0 truncate">{{ l.name }}</a>
              <span v-if="l.fetch_error" class="shrink-0 text-bad" :title="l.fetch_error">拉取失败</span>
              <span v-else-if="l.fetched_at" class="shrink-0 text-muted">{{ fmt.dateTime(l.fetched_at) }}</span>
              <span v-else class="shrink-0 text-muted">还没下载</span>
            </li>
          </ul>
        </Section>
        <Badge v-if="detail.isFetching.value" tone="neutral" class="self-start">更新中</Badge>
      </Card>
    </div>
  </LoadState>

  <ChangeDialog
    v-model:open="formOpen"
    :title="creating ? '新建分流规则' : `修改分流规则 ${form.name}`"
    description="预演时会下载并解析模板，确认策略组和规则数量后再执行。"
    :request="formRequest"
  >
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-1 font-medium">模板来源</legend>
      <div class="flex flex-wrap gap-4">
        <label v-if="creating" class="flex items-center gap-2"><input v-model="form.mode" type="radio" value="preset" class="accent-accent" />ACL4SSR 模板</label>
        <label class="flex items-center gap-2"><input v-model="form.mode" type="radio" value="url" class="accent-accent" />ini 地址</label>
        <label class="flex items-center gap-2"><input v-model="form.mode" type="radio" value="content" class="accent-accent" />粘贴内容</label>
      </div>
    </fieldset>
    <template v-if="form.mode === 'preset'">
      <label class="flex flex-col gap-1.5">
        <span class="font-medium">模板</span>
        <select v-model="form.preset" class="field-input">
          <option v-for="p in presets.data.value?.items ?? []" :key="p.code" :value="p.code">{{ p.name }}</option>
        </select>
        <span v-if="presetPicked" class="text-[13px] text-muted">{{ presetPicked.description }}</span>
      </label>
    </template>
    <Field v-else-if="form.mode === 'url'" v-model="form.url" label="ini 模板地址" hint="subconverter 格式，例如 ACL4SSR 的 Clash/config/*.ini；服务端每 6 小时重新拉取" placeholder="https://" />
    <label v-else class="flex flex-col gap-1.5">
      <span class="font-medium">ini 模板内容</span>
      <textarea v-model="form.content" rows="10" spellcheck="false" class="field-input min-h-40 py-2 font-mono text-[13px]" placeholder="ruleset=🎯 全球直连,https://…&#10;custom_proxy_group=🚀 节点选择`select`.*" />
      <span class="text-[13px] text-muted">只使用 ruleset 和 custom_proxy_group 两类配置，其余忽略。</span>
    </label>
    <Field v-if="creating" v-model="form.code" label="代号" :hint="form.mode === 'preset' ? '留空用模板的代号' : '小写字母、数字和连字符'" />
    <Field v-model="form.name" label="名称" :hint="form.mode === 'preset' && creating ? '留空用模板的名称；用户侧会看到这个名字' : '用户侧会看到这个名字'" />
    <Field v-model="form.description" label="说明" hint="用户侧选择时显示" />
    <label class="flex items-center gap-2"><input v-model="form.selectable" type="checkbox" class="accent-accent" />允许用户在用户侧自选</label>
  </ChangeDialog>
  <ChangeDialog v-if="op" v-model:open="opOpen" :title="op.title" :request="op.request" confirm-only />
</template>
