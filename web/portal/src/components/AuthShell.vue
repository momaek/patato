<script setup lang="ts">
// 登录前的页面：窄栏居中，顶部品牌，底部客服联系方式。
import { useI18n } from 'vue-i18n'
import Brand from './Brand.vue'
import { serviceNameFallback, supportContact } from '../session'
defineProps<{ back?: boolean; help?: string }>()
const { t } = useI18n()
</script>

<template>
  <div class="mx-auto flex min-h-dvh w-full max-w-[420px] flex-col gap-8 px-5 pt-8 pb-10">
    <RouterLink v-if="back" :to="{ name: 'login' }" class="font-medium">← {{ t('common.back') }}</RouterLink>
    <Brand v-else :name="serviceNameFallback" />
    <main class="flex flex-col gap-6">
      <slot />
    </main>
    <p v-if="supportContact" class="mt-auto text-center text-[13px] text-muted">
      {{ help ?? t('common.support', { contact: supportContact }) }}
    </p>
  </div>
</template>
