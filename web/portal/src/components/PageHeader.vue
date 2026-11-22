<script setup lang="ts">
// 页面标题：可选的返回链接、标题、说明，右侧放操作按钮。
import { ArrowLeft } from 'lucide-vue-next'
import type { RouteLocationRaw } from 'vue-router'
defineProps<{ title: string; lead?: string; back?: RouteLocationRaw; backLabel?: string }>()
</script>

<template>
  <div class="flex flex-col gap-3">
    <RouterLink v-if="back" :to="back" class="-ml-1 inline-flex min-h-10 items-center gap-1.5 self-start rounded-md px-1 font-medium text-muted no-underline hover:text-ink">
      <ArrowLeft class="size-4" aria-hidden="true" />{{ backLabel }}
    </RouterLink>
    <div class="flex flex-wrap items-end justify-between gap-3">
      <div class="flex min-w-0 flex-col gap-1">
        <h1 class="text-[26px] leading-tight font-bold tracking-tight"><slot name="title">{{ title }}</slot></h1>
        <p v-if="lead" class="text-muted">{{ lead }}</p>
      </div>
      <slot name="action" />
    </div>
  </div>
</template>
