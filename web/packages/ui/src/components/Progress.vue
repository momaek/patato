<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ value: number; max: number; label: string; valueText?: string }>()
const pct = computed(() => (props.max > 0 ? Math.min(100, (props.value / props.max) * 100) : 0))
const tone = computed(() => (pct.value >= 100 ? 'bg-bad' : pct.value >= 85 ? 'bg-warn' : 'bg-accent'))
</script>

<template>
  <div
    role="progressbar"
    :aria-label="label"
    :aria-valuemin="0"
    :aria-valuemax="max"
    :aria-valuenow="value"
    :aria-valuetext="valueText"
    class="h-2 overflow-hidden rounded-full bg-line-soft"
  >
    <div :class="['h-full rounded-full transition-[width] duration-300', tone]" :style="{ width: pct + '%' }" />
  </div>
</template>
