<script setup lang="ts">
// 节点状态：实心点为在线 / 离线，空心圈为待接入。
withDefaults(defineProps<{ state: 'online' | 'offline' | 'pending' | 'maintenance'; label?: string }>(), {})
const labels = { online: '在线', offline: '离线', pending: '待接入', maintenance: '维护中' }
</script>

<template>
  <span
    :class="[
      'inline-flex items-center gap-1.5 font-medium whitespace-nowrap',
      { online: 'text-ok', offline: 'text-bad', pending: 'text-idle', maintenance: 'text-warn' }[state],
    ]"
  >
    <span
      aria-hidden="true"
      :class="[
        'size-2 rounded-full',
        state === 'pending' ? 'border-2 border-current' : 'bg-current',
      ]"
    />
    {{ label ?? labels[state] }}
  </span>
</template>
