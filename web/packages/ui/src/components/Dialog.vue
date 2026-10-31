<script setup lang="ts">
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
defineProps<{ title: string; description?: string }>()
const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-ink/30" />
      <DialogContent
        class="fixed top-1/2 left-1/2 z-50 flex max-h-[85vh] w-[min(92vw,440px)] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border border-line bg-card p-6 shadow-[0_12px_40px_rgba(24,32,29,0.18)]"
      >
        <div class="flex flex-col gap-1">
          <DialogTitle class="text-lg font-semibold">{{ title }}</DialogTitle>
          <DialogDescription v-if="description" class="text-muted">{{ description }}</DialogDescription>
        </div>
        <slot :close="() => (open = false)" />
        <DialogClose class="absolute top-4 right-4 rounded-md px-2 text-muted hover:bg-bg" aria-label="关闭">✕</DialogClose>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
