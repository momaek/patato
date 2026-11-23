<script setup lang="ts">
import { Toaster } from 'vue-sonner'
import { isDark } from '@patato/ui'
</script>

<template>
  <RouterView />
  <Toaster position="top-right" rich-colors :theme="isDark ? 'dark' : 'light'" />
</template>
