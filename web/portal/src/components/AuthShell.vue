<script setup lang="ts">
// 登录前的页面：桌面端左侧品牌面板 + 右侧表单，手机端只显示表单。底部客服联系方式。
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Gauge, QrCode, ShieldCheck } from 'lucide-vue-next'
import Brand from './Brand.vue'
import { serviceNameFallback, supportContact } from '../session'
defineProps<{ back?: boolean; help?: string }>()
const { t } = useI18n()
const points = [
  { icon: Gauge, key: 'usage' },
  { icon: QrCode, key: 'import' },
  { icon: ShieldCheck, key: 'secure' },
] as const
</script>

<template>
  <div class="grid min-h-dvh lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
    <!-- 品牌面板：只在宽屏显示，纯装饰与卖点说明 -->
    <aside class="relative hidden overflow-hidden bg-[#0b1120] p-12 text-white lg:flex lg:flex-col">
      <div aria-hidden="true" class="pointer-events-none absolute inset-0">
        <div class="absolute -top-32 -left-24 size-[420px] rounded-full bg-[#2563eb]/45 blur-[110px]" />
        <div class="absolute -right-20 bottom-0 size-[360px] rounded-full bg-[#22c55e]/25 blur-[110px]" />
        <svg class="absolute inset-0 size-full opacity-[0.07]" xmlns="http://www.w3.org/2000/svg">
          <defs>
            <pattern id="auth-grid" width="32" height="32" patternUnits="userSpaceOnUse"><path d="M32 0H0v32" fill="none" stroke="white" stroke-width="1" /></pattern>
          </defs>
          <rect width="100%" height="100%" fill="url(#auth-grid)" />
        </svg>
      </div>
      <Brand :name="serviceNameFallback" class="relative" invert />
      <div class="relative mt-auto flex max-w-[420px] flex-col gap-8">
        <p class="text-[28px] leading-tight font-semibold tracking-tight">{{ t('auth.tagline') }}</p>
        <ul class="flex flex-col gap-5">
          <li v-for="p in points" :key="p.key" class="flex items-start gap-3.5">
            <span class="inline-flex size-10 shrink-0 items-center justify-center rounded-xl bg-white/10 ring-1 ring-white/15">
              <component :is="p.icon" class="size-5" aria-hidden="true" />
            </span>
            <span class="flex flex-col">
              <span class="font-semibold">{{ t(`auth.points.${p.key}.title`) }}</span>
              <span class="text-[14px] text-white/70">{{ t(`auth.points.${p.key}.text`) }}</span>
            </span>
          </li>
        </ul>
      </div>
    </aside>

    <div class="flex min-h-dvh flex-col px-5 pt-8 pb-8 sm:px-8">
      <div class="mx-auto flex w-full max-w-[400px] flex-1 flex-col gap-8">
        <RouterLink v-if="back" :to="{ name: 'login' }" class="inline-flex min-h-11 items-center gap-1.5 self-start font-medium">
          <ArrowLeft class="size-4" aria-hidden="true" />{{ t('common.back') }}
        </RouterLink>
        <Brand v-else :name="serviceNameFallback" class="lg:hidden" />
        <main class="flex flex-col gap-6 lg:my-auto">
          <slot />
        </main>
        <p v-if="supportContact" class="mt-auto text-center text-[13px] text-muted">
          {{ help ?? t('common.support', { contact: supportContact }) }}
        </p>
      </div>
    </div>
  </div>
</template>
