<script setup lang="ts">
import { cn } from '../lib/cn'
defineProps<{ class?: string; as?: string }>()
</script>

<template>
  <component :is="as ?? 'div'" :class="cn('rounded-[10px] border border-line bg-card', $props.class)"><slot /></component>
</template>
