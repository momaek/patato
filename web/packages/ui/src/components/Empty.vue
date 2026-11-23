<script setup lang="ts">
import { Inbox } from 'lucide-vue-next'
defineProps<{ title: string }>()
</script>

<template>
  <div class="flex flex-col items-center gap-1 px-5 py-10 text-center">
    <Inbox class="mb-1.5 size-6 text-muted/70" aria-hidden="true" />
    <p class="font-medium">{{ title }}</p>
    <p class="max-w-md text-[13px] text-muted"><slot /></p>
  </div>
</template>
