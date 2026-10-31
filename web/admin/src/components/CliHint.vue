<script setup lang="ts">
// MVP 的 Admin 只读：写操作交给 Agent 或 patato CLI。这张卡告诉运营者该怎么做。
import { Code } from '@patato/ui'
defineProps<{ title: string; command: string }>()
</script>

<template>
  <aside class="flex flex-col gap-2 rounded-[10px] border border-accent/25 bg-accent-soft/50 px-4 py-3.5">
    <p class="font-semibold">{{ title }}</p>
    <p class="text-[13px] text-muted"><slot /></p>
    <Code :text="command" block />
  </aside>
</template>
