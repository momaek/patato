<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { Check, Copy, QrCode } from 'lucide-vue-next'
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

// 二维码始终是白底深色码，深色主题下也一样，扫码更稳
watch(
  [showQr, () => props.url],
  async ([open]) => {
    if (open) qrSrc.value = await QRCode.toDataURL(props.url, { margin: 1, width: 360, color: { dark: '#0f172a', light: '#ffffff' } })
  },
  { immediate: true },
)
</script>

<template>
  <div class="flex flex-col gap-3">
    <p class="rounded-control border border-line-soft bg-bg px-3.5 py-3 font-mono text-[13px] break-all text-muted">{{ fmt.maskToken(url) }}</p>
    <div class="flex flex-wrap gap-2">
      <Button variant="primary" class="min-h-11 max-sm:flex-1" @click="copy">
        <component :is="copied ? Check : Copy" class="size-4" aria-hidden="true" />{{ copied ? t('common.copied') : t('common.copy') }}
      </Button>
      <Button v-if="!noQr" class="min-h-11 max-sm:flex-1" :aria-expanded="showQr" @click="showQr = !showQr">
        <QrCode class="size-4" aria-hidden="true" />{{ showQr ? t('home.hideQr') : t('home.showQr') }}
      </Button>
    </div>
    <span class="sr-only" aria-live="polite">{{ copied ? t('common.copied') : '' }}</span>
    <div v-if="showQr && qrSrc" class="flex flex-col items-center gap-3 rounded-control border border-line-soft bg-bg p-5">
      <img :src="qrSrc" :alt="t('home.qrHint')" class="size-52 rounded-xl bg-white p-2 shadow-card" />
      <span class="text-[13px] text-muted">{{ t('home.qrHint') }}</span>
    </div>
  </div>
</template>
