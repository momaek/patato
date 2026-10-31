<script setup lang="ts">
withDefaults(defineProps<{ tone?: 'bad' | 'warn' | 'ok' | 'accent' }>(), { tone: 'bad' })
const tones = {
  bad: 'border-bad/30 bg-bad-soft text-bad',
  warn: 'border-warn/30 bg-warn-soft text-warn',
  ok: 'border-ok/30 bg-ok-soft text-ok',
  accent: 'border-accent/30 bg-accent-soft text-accent-strong',
}
</script>

<template>
  <div :role="tone === 'bad' ? 'alert' : 'status'" :class="['rounded-lg border px-3.5 py-2.5 text-[13px]', tones[tone]]"><slot /></div>
</template>
