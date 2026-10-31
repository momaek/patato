<script setup lang="ts">
// 表格外壳：卡片边框、窄屏横向滚动。表头和行由调用方写。
defineProps<{ minWidth?: number; label?: string }>()
</script>

<template>
  <div class="overflow-x-auto rounded-[10px] border border-line bg-card">
    <table :aria-label="label" class="w-full border-collapse [&_td]:border-t [&_td]:border-line-soft [&_td]:px-4 [&_td]:py-2.5 [&_th]:border-b [&_th]:border-line [&_th]:px-4 [&_th]:py-2.5 [&_th]:text-left [&_th]:text-[13px] [&_th]:font-medium [&_th]:text-muted [&_tbody_tr:first-child_td]:border-t-0" :style="{ minWidth: (minWidth ?? 640) + 'px' }">
      <slot />
    </table>
  </div>
</template>
