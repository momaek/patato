<script setup lang="ts">
defineProps<{ title: string; lead?: string }>()
</script>

<template>
  <header class="flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-line pb-5">
    <div class="flex min-w-0 flex-col gap-1">
      <slot name="crumbs" />
      <h1 class="text-2xl leading-tight font-semibold tracking-tight">{{ title }}</h1>
      <p v-if="lead" class="max-w-3xl text-muted">{{ lead }}</p>
    </div>
    <slot />
  </header>
</template>
