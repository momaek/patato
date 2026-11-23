<script setup lang="ts">
import { computed } from 'vue'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '../lib/cn'

const button = cva(
  'inline-flex cursor-pointer items-center justify-center gap-2 rounded-control font-medium whitespace-nowrap transition-[background-color,border-color,color,box-shadow] duration-150 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-55 disabled:active:translate-y-0 [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        primary: 'bg-accent text-on-accent shadow-card hover:bg-accent-strong',
        secondary: 'border border-control bg-card text-ink shadow-card hover:bg-line-soft',
        danger: 'border border-bad/40 bg-card text-bad hover:border-bad/70 hover:bg-bad-soft',
        ghost: 'text-muted hover:bg-line-soft hover:text-ink',
      },
      size: {
        md: 'min-h-10 px-4',
        sm: 'min-h-8 px-3 text-[13px]',
        lg: 'min-h-12 px-5 text-[15px]',
        icon: 'size-10 p-0',
      },
      block: { true: 'w-full' },
    },
    defaultVariants: { variant: 'secondary', size: 'md' },
  },
)

type Props = VariantProps<typeof button>
const props = withDefaults(
  defineProps<{
    variant?: Props['variant']
    size?: Props['size']
    block?: boolean
    loading?: boolean
    disabled?: boolean
    type?: 'button' | 'submit'
    class?: string
  }>(),
  { type: 'button' },
)
const classes = computed(() => cn(button({ variant: props.variant, size: props.size, block: props.block }), props.class))
</script>

<template>
  <button :type="type" :class="classes" :disabled="disabled || loading" :aria-busy="loading || undefined">
    <span v-if="loading" class="size-4 animate-spin rounded-full border-2 border-current border-r-transparent" aria-hidden="true" />
    <slot />
  </button>
</template>
