<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@tanstack/vue-query'
import {
  BarChart3, Gift, HardDrive, KeyRound, LayoutDashboard, LogOut, Menu, MessagesSquare, Monitor, Moon, Package,
  Plug, Receipt, Route, ScrollText, Server, ShieldCheck, Sun, TicketPercent, Users, Waypoints, X,
} from 'lucide-vue-next'
import { api, type ApprovalItem, type List, type Status } from '@patato/api'
import { useSession } from '../lib/session'
import Logo from './Logo.vue'
import { cycleTheme, themePref } from '../theme'

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
// 工单徽标：等你处理的工单数，跟总览共用一份数据
const status = useQuery({ queryKey: ['status'], queryFn: () => api.get<Status>('/api/v1/status'), refetchInterval: 60_000 })
const ticketCount = computed(() => status.data.value?.tickets?.needs_human ?? 0)

interface Item { name: string; label: string; icon: Component; match: string[]; count?: number }
const groups = computed<{ label: string; items: Item[] }[]>(() => [
  {
    label: '运营',
    items: [
      { name: 'overview', label: '总览', icon: LayoutDashboard, match: ['overview'] },
      { name: 'approvals', label: '审批', icon: ShieldCheck, match: ['approvals', 'approval'], count: pendingCount.value },
      { name: 'tickets', label: '工单', icon: MessagesSquare, match: ['tickets', 'ticket'], count: ticketCount.value },
    ],
  },
  {
    label: '基础设施',
    items: [
      { name: 'nodes', label: '节点', icon: Server, match: ['nodes', 'node'] },
      { name: 'machines', label: '机器', icon: HardDrive, match: ['machines'] },
      { name: 'routes', label: '线路', icon: Waypoints, match: ['routes'] },
      { name: 'rules', label: '分流规则', icon: Route, match: ['rules'] },
    ],
  },
  {
    label: '业务',
    items: [
      { name: 'plans', label: '套餐', icon: Package, match: ['plans'] },
      { name: 'users', label: '用户', icon: Users, match: ['users'] },
      { name: 'orders', label: '订单', icon: Receipt, match: ['orders'] },
      { name: 'coupons', label: '优惠券', icon: TicketPercent, match: ['coupons'] },
      { name: 'referrals', label: '邀请返佣', icon: Gift, match: ['referrals'] },
      { name: 'reports', label: '报表', icon: BarChart3, match: ['reports'] },
    ],
  },
  {
    label: '系统',
    items: [
      { name: 'integrations', label: '集成', icon: Plug, match: ['integrations'] },
      { name: 'audit', label: '审计日志', icon: ScrollText, match: ['audit'] },
    ],
  },
])
const badgeTotal = computed(() => pendingCount.value + ticketCount.value)
const isActive = (i: Item) => i.match.includes(String(route.name))

const themeMeta = computed(() => ({
  system: { icon: Monitor, label: '跟随系统' },
  light: { icon: Sun, label: '浅色' },
  dark: { icon: Moon, label: '深色' },
})[themePref.value])
const initial = computed(() => [...(session.me?.operator.name ?? '?')][0]?.toUpperCase())

async function logout() {
  await session.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="flex min-h-dvh flex-col lg:flex-row">
    <a href="#main" class="sr-only z-50 rounded-md bg-card px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2">跳到主要内容</a>

    <!-- 窄屏顶栏 -->
    <header class="sticky top-0 z-30 flex items-center justify-between gap-2 border-b border-line bg-card/90 px-4 py-2.5 backdrop-blur lg:hidden">
      <RouterLink :to="{ name: 'overview' }" class="flex items-center gap-2 text-ink no-underline"><Logo /></RouterLink>
      <button
        type="button"
        class="relative inline-flex size-10 items-center justify-center rounded-lg border border-line text-ink transition-colors hover:bg-line-soft"
        :aria-expanded="menuOpen"
        aria-controls="side-nav"
        :aria-label="menuOpen ? '关闭菜单' : '打开菜单'"
        @click="menuOpen = !menuOpen"
      >
        <component :is="menuOpen ? X : Menu" class="size-5" aria-hidden="true" />
        <span v-if="badgeTotal && !menuOpen" class="absolute -top-1 -right-1 min-w-4.5 rounded-full bg-warn px-1 text-[11px] leading-4.5 font-semibold text-card tabular">{{ badgeTotal }}</span>
      </button>
    </header>
    <div v-if="menuOpen" class="fixed inset-0 z-30 bg-scrim lg:hidden" aria-hidden="true" @click="menuOpen = false" />

    <nav
      id="side-nav"
      aria-label="主导航"
      :class="[
        'fixed inset-y-0 left-0 z-40 flex w-[240px] flex-col border-r border-line bg-card transition-transform duration-200 lg:sticky lg:top-0 lg:z-auto lg:h-dvh lg:w-[232px] lg:shrink-0 lg:translate-x-0',
        menuOpen ? 'visible translate-x-0 shadow-pop' : 'invisible -translate-x-full lg:visible',
      ]"
    >
      <div class="flex h-14 items-center px-5">
        <RouterLink :to="{ name: 'overview' }" class="flex items-center gap-2 text-ink no-underline"><Logo /></RouterLink>
        <span class="ml-2 rounded border border-line px-1.5 py-px font-mono text-[10.5px] tracking-wide text-muted uppercase">admin</span>
      </div>

      <div class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-3 py-3">
        <div v-for="g in groups" :key="g.label" class="flex flex-col gap-0.5">
          <p class="px-2.5 pb-1 text-[11px] font-medium tracking-wider text-muted uppercase">{{ g.label }}</p>
          <RouterLink
            v-for="item in g.items"
            :key="item.name"
            :to="{ name: item.name }"
            :aria-current="isActive(item) ? 'page' : undefined"
            :class="[
              'group relative flex min-h-9 items-center gap-2.5 rounded-lg px-2.5 no-underline transition-colors duration-150',
              isActive(item) ? 'bg-accent-soft font-medium text-ink' : 'text-muted hover:bg-line-soft hover:text-ink',
            ]"
          >
            <span v-if="isActive(item)" class="absolute top-2 bottom-2 -left-3 w-[3px] rounded-r bg-accent" aria-hidden="true" />
            <component :is="item.icon" :class="['size-4 shrink-0', isActive(item) ? 'text-accent' : 'text-muted group-hover:text-ink']" aria-hidden="true" />
            <span class="flex-1">{{ item.label }}</span>
            <span v-if="item.count" class="min-w-5 rounded-full bg-warn-soft px-1.5 text-center text-xs leading-5 font-semibold text-warn tabular">
              {{ item.count }}<span class="sr-only"> 待处理</span>
            </span>
          </RouterLink>
        </div>
      </div>

      <div class="flex flex-col gap-1 border-t border-line p-3">
        <button
          type="button"
          class="flex min-h-9 items-center gap-2.5 rounded-lg px-2.5 text-muted transition-colors hover:bg-line-soft hover:text-ink"
          :aria-label="`主题：${themeMeta.label}，点击切换`"
          @click="cycleTheme()"
        >
          <component :is="themeMeta.icon" class="size-4" aria-hidden="true" />
          <span class="flex-1 text-left">主题</span>
          <span class="text-[12px]">{{ themeMeta.label }}</span>
        </button>
        <div class="mt-1 flex items-center gap-2.5 rounded-lg px-2.5 py-1.5">
          <span class="inline-flex size-8 shrink-0 items-center justify-center rounded-full bg-accent-soft text-[13px] font-semibold text-accent" aria-hidden="true">{{ initial }}</span>
          <div class="flex min-w-0 flex-1 flex-col leading-tight">
            <span class="truncate font-medium">{{ session.me?.operator.name }}</span>
            <span class="text-[12px] text-muted">passkey 登录</span>
          </div>
          <RouterLink
            :to="{ name: 'account' }"
            :aria-current="route.name === 'account' ? 'page' : undefined"
            class="inline-flex size-8 items-center justify-center rounded-md text-muted transition-colors hover:bg-line-soft hover:text-ink aria-[current=page]:text-accent"
            aria-label="账户与凭证"
            title="账户与凭证"
          >
            <KeyRound class="size-4" aria-hidden="true" />
          </RouterLink>
          <button type="button" class="inline-flex size-8 items-center justify-center rounded-md text-muted transition-colors hover:bg-bad-soft hover:text-bad" aria-label="退出登录" title="退出登录" @click="logout">
            <LogOut class="size-4" aria-hidden="true" />
          </button>
        </div>
      </div>
    </nav>

    <main id="main" class="mx-auto flex w-full max-w-[1440px] min-w-0 flex-1 flex-col gap-7 px-4 pt-6 pb-14 sm:px-6 lg:px-9 lg:pt-8">
      <RouterView />
    </main>
  </div>
</template>
