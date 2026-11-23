<script setup lang="ts">
// 状态标签。状态除颜色外始终带文字。
export type Tone = 'ok' | 'warn' | 'bad' | 'idle' | 'accent' | 'neutral'
withDefaults(defineProps<{ tone?: Tone }>(), { tone: 'neutral' })
const tones: Record<Tone, string> = {
  ok: 'bg-ok-soft text-ok',
  warn: 'bg-warn-soft text-warn',
  bad: 'bg-bad-soft text-bad',
  idle: 'bg-idle-soft text-idle',
  accent: 'bg-accent-soft text-accent-strong',
  neutral: 'bg-line-soft text-muted',
}
</script>

<template>
  <span :class="['inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs leading-4 font-medium whitespace-nowrap ring-1 ring-current/15 ring-inset', tones[tone]]"><slot /></span>
</template>
