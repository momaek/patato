<script setup lang="ts">
// 登录后的页面：顶部品牌与退出，下面一条导航。MVP 只有订阅一页，续费、工单、邀请在二期加入导航。
import { useI18n } from 'vue-i18n'
import Brand from './Brand.vue'
import { useLogout } from '../session'
defineProps<{ serviceName: string; wide?: boolean }>()
const { t } = useI18n()
const logout = useLogout()
</script>

<template>
  <div :class="['mx-auto flex min-h-dvh w-full flex-col gap-7 px-5 pt-5 pb-10 md:px-8 md:pt-7', wide ? 'max-w-[1120px]' : 'max-w-[560px]']">
    <header class="flex items-center justify-between gap-3">
      <Brand :name="serviceName" />
      <button type="button" class="rounded-md px-2 py-1 font-medium text-accent hover:bg-accent-soft" @click="logout">{{ t('common.logout') }}</button>
    </header>
    <nav :aria-label="t('home.nav')" class="-mt-2 flex gap-1 border-b border-line">
      <RouterLink
        v-for="item in [{ name: 'home', label: t('home.nav') }, { name: 'password', label: t('home.account') }]"
        :key="item.name"
        :to="{ name: item.name }"
        class="-mb-px border-b-2 border-transparent px-3 py-2 font-medium text-muted no-underline hover:text-ink"
        exact-active-class="!border-accent !text-ink"
      >
        {{ item.label }}
      </RouterLink>
    </nav>
    <main class="flex flex-col gap-7"><slot /></main>
  </div>
</template>
