<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ name: string; invert?: boolean }>()
const { t } = useI18n()
const label = computed(() => props.name || t('brand.fallback'))
const initial = computed(() => (props.name ? [...props.name][0]!.toUpperCase() : 'S'))
</script>

<template>
  <div class="flex items-center gap-2.5">
    <span
      aria-hidden="true"
      class="inline-flex size-8 items-center justify-center rounded-[10px] bg-gradient-to-br from-[#3b82f6] to-[#1d4ed8] text-[15px] font-bold text-white shadow-[0_2px_8px_rgb(37_99_235/0.35)]"
    >{{ initial }}</span>
    <span :class="['text-[16px] font-semibold tracking-tight', invert ? 'text-white' : 'text-ink']">{{ label }}</span>
  </div>
</template>
