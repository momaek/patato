<script setup lang="ts">
// 表格外壳：卡片边框、窄屏横向滚动。表头和行由调用方写。
defineProps<{ minWidth?: number; label?: string }>()
</script>

<template>
  <div class="overflow-x-auto rounded-xl border border-line bg-card shadow-card">
    <table
      :aria-label="label"
      class="w-full border-collapse [&_td]:border-t [&_td]:border-line-soft [&_td]:px-4 [&_td]:py-2.5 [&_td]:align-middle [&_th]:border-b [&_th]:border-line [&_th]:bg-line-soft/60 [&_th]:px-4 [&_th]:py-2 [&_th]:text-left [&_th]:text-[12px] [&_th]:font-medium [&_th]:tracking-wide [&_th]:whitespace-nowrap [&_th]:text-muted [&_tbody_tr]:transition-colors [&_tbody_tr:hover]:bg-line-soft/50 [&_tbody_tr:first-child_td]:border-t-0 [&_td_a]:text-ink [&_td_a:hover]:text-accent" :style="{ minWidth: (minWidth ?? 640) + 'px' }">
      <slot />
    </table>
  </div>
</template>
