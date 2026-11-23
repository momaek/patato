<script setup lang="ts">
// 数据加载的三种状态：加载中、出错（带重试）、正常内容。
import { CircleAlert } from 'lucide-vue-next'
defineProps<{ loading?: boolean; error?: unknown; message?: string }>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div v-if="loading" class="flex flex-col gap-2 py-2" aria-busy="true" aria-live="polite">
    <span class="sr-only">加载中</span>
    <div v-for="i in 3" :key="i" class="h-10 animate-pulse rounded-control bg-line-soft" />
  </div>
  <div v-else-if="error" role="alert" class="flex flex-wrap items-center gap-3 rounded-card border border-bad/30 bg-bad-soft px-4 py-3 text-bad">
    <CircleAlert class="size-4 shrink-0" aria-hidden="true" />
    <span class="flex-1">{{ message ?? '数据没有加载出来' }}</span>
    <button type="button" class="cursor-pointer rounded-md border border-bad/40 bg-card px-3 py-1 text-[13px] font-medium transition-colors hover:bg-bad-soft" @click="$emit('retry')">重试</button>
  </div>
  <slot v-else />
</template>
