<script setup lang="ts">
// 页面里的一个区块：标题、计数或说明、右侧链接。
import { useId } from 'vue'
defineProps<{ title: string; meta?: string }>()
const id = useId()
</script>

<template>
  <section :aria-labelledby="id" class="flex min-w-0 flex-col gap-3">
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <div class="flex items-baseline gap-2.5">
        <h2 :id="id" class="text-[15px] font-semibold">{{ title }}</h2>
        <span v-if="meta" class="text-[13px] text-muted tabular">{{ meta }}</span>
      </div>
      <slot name="action" />
    </div>
    <slot />
  </section>
</template>
