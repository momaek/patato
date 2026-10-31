<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <div class="flex flex-col gap-1 px-5 py-8 text-center">
    <p class="font-medium">{{ title }}</p>
    <p class="text-[13px] text-muted"><slot /></p>
  </div>
</template>
