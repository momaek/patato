<script setup lang="ts">
// 命令、change_id 这类需要原样复制的内容；带复制按钮。
import { ref } from 'vue'
const props = defineProps<{ text: string; block?: boolean }>()
const copied = ref(false)
async function copy() {
  await navigator.clipboard.writeText(props.text)
  copied.value = true
  setTimeout(() => (copied.value = false), 1600)
}
</script>

<template>
  <div v-if="block" class="flex items-start gap-2 rounded-lg border border-line bg-bg p-3">
    <pre class="min-w-0 flex-1 overflow-x-auto font-mono text-[13px] leading-relaxed whitespace-pre-wrap break-all">{{ text }}</pre>
    <button type="button" class="shrink-0 rounded-md px-2 py-1 text-[13px] font-medium text-accent hover:bg-accent-soft" @click="copy">
      {{ copied ? '已复制' : '复制' }}
    </button>
  </div>
  <code v-else class="rounded bg-bg px-1.5 py-0.5 font-mono text-[13px] break-all">{{ text }}</code>
</template>
