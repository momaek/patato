<script setup lang="ts">
// 登录后的页面：顶部品牌与退出，下面一条导航。续费在开通在线支付后才显示。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Brand from './Brand.vue'
import { useLogout, usePortalConfig } from '../session'
defineProps<{ serviceName: string; wide?: boolean }>()
const { t } = useI18n()
const logout = useLogout()
const config = usePortalConfig()
const items = computed(() =>
  [
    { name: 'home', label: t('nav.home'), match: ['home'] },
    config.data.value?.payment ? { name: 'buy', label: t('nav.buy'), match: ['buy'] } : null,
    { name: 'orders', label: t('nav.orders'), match: ['orders', 'order'] },
    { name: 'tickets', label: t('nav.tickets'), match: ['tickets', 'ticket'] },
    { name: 'invite', label: t('nav.invite'), match: ['invite'] },
    { name: 'password', label: t('nav.account'), match: ['password'] },
  ].filter((x): x is { name: string; label: string; match: string[] } => !!x),
)
</script>

<template>
  <div :class="['mx-auto flex min-h-dvh w-full flex-col gap-7 px-5 pt-5 pb-10 md:px-8 md:pt-7', wide ? 'max-w-[1120px]' : 'max-w-[560px]']">
    <header class="flex items-center justify-between gap-3">
      <Brand :name="serviceName" />
      <button type="button" class="rounded-md px-2 py-1 font-medium text-accent hover:bg-accent-soft" @click="logout">{{ t('common.logout') }}</button>
    </header>
    <nav :aria-label="t('home.nav')" class="-mt-2 flex gap-1 overflow-x-auto border-b border-line">
      <RouterLink
        v-for="item in items"
        :key="item.name"
        :to="{ name: item.name }"
        class="-mb-px shrink-0 border-b-2 border-transparent px-3 py-2 font-medium whitespace-nowrap text-muted no-underline hover:text-ink"
        :class="item.match.includes(String($route.name)) ? '!border-accent !text-ink' : ''"
        :aria-current="item.match.includes(String($route.name)) ? 'page' : undefined"
      >
        {{ item.label }}
      </RouterLink>
    </nav>
    <main class="flex flex-col gap-7"><slot /></main>
  </div>
</template>
