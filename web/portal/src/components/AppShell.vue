<script setup lang="ts">
// 登录后的页面：顶部吸附的页头（品牌、桌面端导航、深浅色、账户、退出），手机端底部标签栏。
// 续费在开通在线支付后才显示。底部标签栏最多 5 项，修改密码放在页头的账户按钮里。
import { computed, type Component } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Gift, House, LifeBuoy, LogOut, Monitor, Moon, ReceiptText, ShoppingBag, Sun, UserRound } from 'lucide-vue-next'
import Brand from './Brand.vue'
import { useLogout, usePortalConfig } from '../session'
import { cycleTheme, themePref } from '../theme'

defineProps<{ serviceName: string; wide?: boolean }>()
const { t } = useI18n()
const route = useRoute()
const logout = useLogout()
const config = usePortalConfig()

type Item = { name: string; label: string; short: string; icon: Component; match: string[] }
const items = computed(() =>
  (
    [
      { name: 'home', label: t('nav.home'), short: t('nav.home'), icon: House, match: ['home', 'reset-link'] },
      config.data.value?.payment ? { name: 'buy', label: t('nav.buy'), short: t('nav.buyShort'), icon: ShoppingBag, match: ['buy'] } : null,
      { name: 'orders', label: t('nav.orders'), short: t('nav.orders'), icon: ReceiptText, match: ['orders', 'order'] },
      { name: 'tickets', label: t('nav.tickets'), short: t('nav.tickets'), icon: LifeBuoy, match: ['tickets', 'ticket'] },
      { name: 'invite', label: t('nav.invite'), short: t('nav.invite'), icon: Gift, match: ['invite'] },
    ] as (Item | null)[]
  ).filter((x): x is Item => !!x),
)
const active = (i: Item) => i.match.includes(String(route.name))
const themeIcon = computed(() => ({ system: Monitor, light: Sun, dark: Moon })[themePref.value])
const themeLabel = computed(() => t('theme.switch', { current: t(`theme.${themePref.value}`) }))
</script>

<template>
  <div class="flex min-h-dvh flex-col">
    <header class="sticky top-0 z-30 border-b border-line bg-bg/80 backdrop-blur-md">
      <div class="mx-auto flex h-16 w-full max-w-[1160px] items-center gap-6 px-4 md:px-8">
        <RouterLink :to="{ name: 'home' }" class="shrink-0 rounded-lg text-ink no-underline hover:text-ink">
          <Brand :name="serviceName" />
        </RouterLink>
        <nav :aria-label="t('home.nav')" class="hidden flex-1 items-center gap-1 md:flex">
          <RouterLink
            v-for="item in items"
            :key="item.name"
            :to="{ name: item.name }"
            :aria-current="active(item) ? 'page' : undefined"
            :class="[
              'inline-flex min-h-10 items-center gap-2 rounded-control px-3 font-medium whitespace-nowrap no-underline transition-colors duration-150',
              active(item) ? 'bg-accent-soft text-accent hover:text-accent' : 'text-muted hover:bg-card hover:text-ink',
            ]"
          >
            <component :is="item.icon" class="size-4" aria-hidden="true" />
            {{ item.label }}
          </RouterLink>
        </nav>
        <div class="ml-auto flex items-center gap-1">
          <button
            type="button"
            class="inline-flex size-11 items-center justify-center rounded-control text-muted transition-colors duration-150 hover:bg-card hover:text-ink"
            :aria-label="themeLabel"
            :title="themeLabel"
            @click="cycleTheme"
          >
            <component :is="themeIcon" class="size-5" aria-hidden="true" />
          </button>
          <RouterLink
            :to="{ name: 'password' }"
            :aria-current="route.name === 'password' ? 'page' : undefined"
            :aria-label="t('nav.account')"
            :title="t('nav.account')"
            :class="[
              'inline-flex size-11 items-center justify-center rounded-control no-underline transition-colors duration-150',
              route.name === 'password' ? 'bg-accent-soft text-accent hover:text-accent' : 'text-muted hover:bg-card hover:text-ink',
            ]"
          >
            <UserRound class="size-5" aria-hidden="true" />
          </RouterLink>
          <button
            type="button"
            class="inline-flex min-h-11 min-w-11 items-center justify-center gap-2 rounded-control px-2 font-medium text-muted transition-colors duration-150 hover:bg-card hover:text-ink sm:px-3"
            @click="logout"
          >
            <LogOut class="size-5" aria-hidden="true" />
            <span class="max-sm:sr-only">{{ t('common.logout') }}</span>
          </button>
        </div>
      </div>
    </header>

    <main :class="['mx-auto flex w-full flex-1 flex-col gap-6 px-4 pt-6 pb-28 md:px-8 md:pt-8 md:pb-12', wide ? 'max-w-[1160px]' : 'max-w-[680px]']">
      <slot />
    </main>

    <!-- 手机端底部标签栏 -->
    <nav
      :aria-label="t('home.nav')"
      class="fixed inset-x-0 bottom-0 z-30 border-t border-line bg-card/90 pb-[env(safe-area-inset-bottom)] backdrop-blur-md md:hidden"
    >
      <ul class="mx-auto flex max-w-[560px]">
        <li v-for="item in items" :key="item.name" class="flex-1">
          <RouterLink
            :to="{ name: item.name }"
            :aria-current="active(item) ? 'page' : undefined"
            :class="[
              'flex min-h-14 flex-col items-center justify-center gap-0.5 text-[12px] font-medium no-underline transition-colors duration-150',
              active(item) ? 'text-accent hover:text-accent' : 'text-muted hover:text-ink',
            ]"
          >
            <component :is="item.icon" class="size-[22px]" :stroke-width="active(item) ? 2.25 : 1.75" aria-hidden="true" />
            {{ item.short }}
          </RouterLink>
        </li>
      </ul>
    </nav>
  </div>
</template>
