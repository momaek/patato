<script setup lang="ts">
defineProps<{ title: string; lead?: string }>()
</script>

<template>
  <header class="flex flex-wrap items-end justify-between gap-3">
    <div class="flex flex-col gap-1.5">
      <slot name="crumbs" />
      <h1 class="text-[28px] leading-tight font-semibold tracking-tight">{{ title }}</h1>
      <p v-if="lead" class="text-muted">{{ lead }}</p>
    </div>
    <slot />
  </header>
</template>
