<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ name: string }>()
const { t } = useI18n()
const label = computed(() => props.name || t('brand.fallback'))
const initial = computed(() => (props.name ? [...props.name][0]!.toUpperCase() : 'S'))
</script>

<template>
  <div class="flex items-center gap-2.5">
    <span aria-hidden="true" class="inline-flex size-7 items-center justify-center rounded-[7px] bg-accent text-sm font-semibold text-white">{{ initial }}</span>
    <span class="font-semibold">{{ label }}</span>
  </div>
</template>
