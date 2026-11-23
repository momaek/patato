<script setup lang="ts">
import { X } from 'lucide-vue-next'
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
defineProps<{ title: string; description?: string }>()
const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-scrim backdrop-blur-[2px] data-[state=open]:animate-[p-fade_150ms_ease-out]" />
      <DialogContent
        class="fixed top-1/2 left-1/2 z-50 flex max-h-[85vh] w-[min(92vw,460px)] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-card border border-line bg-card p-6 shadow-pop data-[state=open]:animate-[p-pop_180ms_cubic-bezier(0.16,1,0.3,1)]"
      >
        <div class="flex flex-col gap-1">
          <DialogTitle class="pr-8 text-lg font-semibold">{{ title }}</DialogTitle>
          <DialogDescription v-if="description" class="text-muted">{{ description }}</DialogDescription>
        </div>
        <slot :close="() => (open = false)" />
        <DialogClose class="absolute top-4 right-4 inline-flex size-8 cursor-pointer items-center justify-center rounded-md text-muted transition-colors hover:bg-line-soft hover:text-ink" aria-label="关闭"><X class="size-4" aria-hidden="true" /></DialogClose>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
