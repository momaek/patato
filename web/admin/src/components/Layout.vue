<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import { api, type ApprovalItem, type List } from '@patato/api'
import { useSession } from '../lib/session'

const session = useSession()
const router = useRouter()
const route = useRoute()
const menuOpen = ref(false)
watch(() => route.fullPath, () => (menuOpen.value = false))

// 导航上的待审批数，半分钟刷新一次
const pending = useQuery({
  queryKey: ['approvals', 'pending'],
  queryFn: () => api.get<List<ApprovalItem>>('/api/v1/approvals', { query: { status: 'pending' } }),
  refetchInterval: 30_000,
})
const pendingCount = computed(() => (pending.data.value?.items ?? []).filter((a) => a.status === 'pending').length)

const items = computed(() => [
  { to: { name: 'overview' }, label: '总览', match: ['overview'] },
  { to: { name: 'approvals' }, label: '审批', match: ['approvals', 'approval'], count: pendingCount.value },
  { to: { name: 'nodes' }, label: '节点', match: ['nodes', 'node'] },
  { to: { name: 'machines' }, label: '机器', match: ['machines'] },
  { to: { name: 'plans' }, label: '套餐', match: ['plans'] },
  { to: { name: 'users' }, label: '用户', match: ['users'] },
  { to: { name: 'audit' }, label: '审计日志', match: ['audit'] },
])

async function logout() {
  await session.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="flex min-h-dvh flex-col lg:flex-row">
    <nav aria-label="主导航" class="flex flex-col border-b border-line bg-card lg:sticky lg:top-0 lg:h-dvh lg:w-[220px] lg:shrink-0 lg:gap-7 lg:border-r lg:border-b-0 lg:px-3.5 lg:py-6">
      <div class="flex items-center justify-between gap-2 px-4 py-3 lg:px-2.5 lg:py-0">
        <RouterLink :to="{ name: 'overview' }" class="flex items-center gap-2.5 text-ink no-underline">
          <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true"><path d="M4 12.5C3 7.5 7 3 12 3.5c4.6.4 7.2 4 6.4 8.4-.8 4.6-4.6 7.4-8.8 6.8C6.6 18.2 4.6 15.7 4 12.5Z" fill="#2B44C4" /></svg>
          <span class="text-lg font-semibold tracking-tight">patato</span>
        </RouterLink>
        <button type="button" class="rounded-md border border-control px-3 py-1.5 font-medium lg:hidden" :aria-expanded="menuOpen" aria-controls="nav-list" @click="menuOpen = !menuOpen">
          菜单<span v-if="pendingCount" class="ml-1.5 rounded-full bg-warn-soft px-1.5 text-xs font-semibold text-warn">{{ pendingCount }}</span>
        </button>
      </div>
      <div id="nav-list" :class="['flex-col gap-0.5 px-3 pb-3 lg:flex lg:px-0 lg:pb-0', menuOpen ? 'flex' : 'hidden']">
        <RouterLink
          v-for="item in items"
          :key="item.label"
          :to="item.to"
          :aria-current="item.match.includes(String(route.name)) ? 'page' : undefined"
          :class="[
            'flex items-center justify-between rounded-md px-2.5 py-2 no-underline',
            item.match.includes(String(route.name)) ? 'bg-accent-soft font-semibold text-accent-strong' : 'text-ink hover:bg-bg',
          ]"
        >
          {{ item.label }}
          <span v-if="item.count" class="rounded-full bg-warn-soft px-1.5 text-xs font-semibold text-warn">{{ item.count }}</span>
        </RouterLink>
      </div>
      <div :class="['flex-col gap-1 border-t border-line px-5 py-3 lg:mt-auto lg:flex lg:px-2.5 lg:pt-3 lg:pb-0', menuOpen ? 'flex' : 'hidden']">
        <span class="font-medium">{{ session.me?.operator.name }}</span>
        <span class="text-xs text-muted">已用 passkey 登录</span>
        <div class="mt-1 flex gap-3 text-[13px]">
          <RouterLink :to="{ name: 'account' }">账户与凭证</RouterLink>
          <button type="button" class="text-accent hover:text-accent-strong" @click="logout">退出</button>
        </div>
      </div>
    </nav>
    <main class="flex min-w-0 flex-1 flex-col gap-9 px-4 pt-6 pb-14 sm:px-6 lg:px-10 lg:pt-8">
      <RouterView />
    </main>
  </div>
</template>
