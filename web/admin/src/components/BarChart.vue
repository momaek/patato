<script setup lang="ts">
// 单一序列的按天柱状图：一种颜色（accent），柱顶 4px 圆角、柱间 2px 间隔，悬停显示当天数值。
// 只有一个序列，所以没有图例，标题说明是什么；同一份数据另有表格视图。
import { computed, ref } from 'vue'

const props = defineProps<{ label: string; points: { day: string; value: number }[]; format: (v: number) => string }>()
const max = computed(() => Math.max(1, ...props.points.map((p) => p.value)))
const hover = ref<number | null>(null)
const ticks = computed(() => {
  const n = props.points.length
  if (n === 0) return []
  return [0, Math.floor((n - 1) / 2), n - 1].filter((v, i, a) => a.indexOf(v) === i)
})
const active = computed(() => (hover.value === null ? null : props.points[hover.value]))
</script>

<template>
  <figure class="flex flex-col gap-2">
    <figcaption class="sr-only">{{ label }}</figcaption>
    <div class="relative">
      <p class="absolute -top-1 right-0 text-[13px] text-muted tabular" aria-hidden="true">最高 {{ format(max) }}</p>
      <div class="flex h-40 items-end gap-[2px] border-b border-line pt-5" role="img" :aria-label="label" @mouseleave="hover = null">
        <div
          v-for="(p, i) in points"
          :key="p.day"
          class="relative flex h-full flex-1 cursor-default items-end rounded-t-[4px] outline-offset-0"
          @mouseenter="hover = i"
          @focus="hover = i"
          @blur="hover = null"
          tabindex="0"
          :aria-label="`${p.day}：${format(p.value)}`"
        >
          <div
            :class="['w-full rounded-t-[4px] bg-accent transition-opacity duration-150', hover === null || hover === i ? 'opacity-100' : 'opacity-35']"
            :style="{ height: p.value > 0 ? `max(2px, ${(p.value / max) * 100}%)` : '0' }"
          />
        </div>
      </div>
      <div v-if="active" class="pointer-events-none absolute top-0 left-0 rounded-md border border-line bg-raised px-2.5 py-1.5 text-[13px] shadow-pop">
        <span class="text-muted">{{ active.day }}</span> <span class="font-medium tabular">{{ format(active.value) }}</span>
      </div>
    </div>
    <div class="flex justify-between text-[12px] text-muted tabular" aria-hidden="true">
      <span v-for="t in ticks" :key="t">{{ points[t]?.day.slice(5) }}</span>
    </div>
  </figure>
</template>
