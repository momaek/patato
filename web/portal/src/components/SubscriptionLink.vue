<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { fmt } from '@patato/api'
import { Button } from '@patato/ui'

const props = defineProps<{ url: string; noQr?: boolean }>()
const { t } = useI18n()
const copied = ref(false)
const showQr = ref(false)
const qrSrc = ref('')

async function copy() {
  await navigator.clipboard.writeText(props.url)
  copied.value = true
  setTimeout(() => (copied.value = false), 1600)
}

watch(
  [showQr, () => props.url],
  async ([open]) => {
    if (open) qrSrc.value = await QRCode.toDataURL(props.url, { margin: 1, width: 360, color: { dark: '#18201d', light: '#ffffff' } })
  },
  { immediate: true },
)
</script>

<template>
  <div class="flex flex-col gap-3">
    <p class="rounded-lg border border-line bg-card px-3 py-2.5 font-mono text-[13px] break-all">{{ fmt.maskToken(url) }}</p>
    <div class="flex flex-wrap gap-2">
      <Button variant="primary" @click="copy">{{ copied ? t('common.copied') : t('common.copy') }}</Button>
      <Button v-if="!noQr" :aria-expanded="showQr" @click="showQr = !showQr">{{ showQr ? t('home.hideQr') : t('home.showQr') }}</Button>
    </div>
    <div v-if="showQr && qrSrc" class="flex flex-col items-center gap-2 rounded-lg border border-line bg-card p-4">
      <img :src="qrSrc" :alt="t('home.qrHint')" class="size-56" />
      <span class="text-[13px] text-muted">{{ t('home.qrHint') }}</span>
    </div>
  </div>
</template>
