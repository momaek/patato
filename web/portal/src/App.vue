<script setup lang="ts">
import { Toaster } from 'vue-sonner'
</script>

<template>
  <RouterView />
  <Toaster position="top-center" :toast-options="{ style: { fontFamily: 'inherit' } }" />
</template>
