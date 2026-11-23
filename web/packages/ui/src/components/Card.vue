<script setup lang="ts">
import { cn } from '../lib/cn'
defineProps<{ class?: string; as?: string }>()
</script>

<template>
  <component :is="as ?? 'div'" :class="cn('rounded-card border border-line bg-card shadow-card', $props.class)"><slot /></component>
</template>
