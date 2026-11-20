<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { errorText, fmt } from '@patato/api'
import { Alert, Badge, Card, LoadState, Progress, StatusDot, type Tone } from '@patato/ui'
import AppShell from '../components/AppShell.vue'
import RulePicker from '../components/RulePicker.vue'
import SubscriptionLink from '../components/SubscriptionLink.vue'
import { api } from '@patato/api'
import { useAuthed, usePortalConfig, useSubscription } from '../session'
import { clients } from '../clients'

const { t } = useI18n()
const { data: sub, isPending, error, refetch, serviceName } = useSubscription()

const config = usePortalConfig()
const usage = useAuthed(['me', 'traffic'], () => api.get<{ items: { day: string; bytes: number }[] }>('/api/v1/me/traffic'))
const usageMax = computed(() => Math.max(1, ...(usage.data.value?.items ?? []).map((d) => d.bytes)))
const usageTotal = computed(() => (usage.data.value?.items ?? []).reduce((s, d) => s + d.bytes, 0))

const stateTone: Record<string, Tone> = { active: 'ok', expired: 'bad', over_quota: 'warn', banned: 'bad' }

const remaining = computed(() => {
  const s = sub.value
  if (!s) return ''
  const left = fmt.bytes(s.remaining_bytes)
  return s.next_reset_at ? t('home.remaining', { left, date: fmt.monthDay(s.next_reset_at) }) : t('home.remainingNoReset', { left })
})

const expires = computed(() => {
  const s = sub.value
  if (!s?.expires_at) return t('home.never')
  const date = fmt.date(s.expires_at)
  return s.days_left ? t('home.expiresValue', { date, days: s.days_left }) : t('home.expiresToday', { date })
})

function copyFor(name: string) {
  if (!sub.value) return
  navigator.clipboard.writeText(sub.value.subscription_url).then(
    () => toast.success(`订阅链接已复制，在 ${name} 里粘贴添加`),
    (e) => toast.error(errorText(e)),
  )
}
</script>

<template>
  <AppShell :service-name="serviceName" wide>
    <LoadState :loading="isPending" :error="error" :message="error ? errorText(error) : undefined" @retry="refetch()">
      <div v-if="sub" class="flex flex-wrap items-start gap-7">
        <div class="flex min-w-0 flex-[3_1_480px] flex-col gap-7">
          <Card as="section" aria-labelledby="plan-h" class="flex flex-col gap-5 p-5">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <h1 id="plan-h" class="text-2xl font-semibold">{{ sub.plan?.name ?? t('home.noPlan') }}</h1>
              <Badge :tone="stateTone[sub.state]">{{ t(`home.state.${sub.state}`) }}</Badge>
            </div>
            <Alert v-if="sub.state !== 'active'" :tone="sub.state === 'over_quota' ? 'warn' : 'bad'">{{ t(`home.stateHint.${sub.state}`) }}</Alert>
            <RouterLink v-if="config.data.value?.payment" :to="{ name: 'buy' }" class="self-start no-underline">
              <span class="inline-flex min-h-10 items-center rounded-lg bg-accent px-4 font-medium text-white hover:bg-accent-strong">{{ sub.plan ? t('home.renew') : t('home.buyPlan') }}</span>
            </RouterLink>
            <div class="flex flex-col gap-2">
              <div class="flex flex-wrap items-baseline justify-between gap-2">
                <span class="text-muted">{{ t('home.traffic') }}</span>
                <span class="tabular">
                  <strong class="text-lg font-semibold">{{ fmt.bytes(sub.used_bytes) }}</strong>
                  <span class="ml-1 text-muted">{{ t('home.trafficOf', { total: fmt.bytes(sub.transfer_bytes) }) }}</span>
                </span>
              </div>
              <Progress :value="sub.used_bytes" :max="sub.transfer_bytes" :label="t('home.traffic')" :value-text="`${fmt.bytes(sub.used_bytes)} / ${fmt.bytes(sub.transfer_bytes)}`" />
              <span class="text-[13px] text-muted">{{ remaining }}</span>
            </div>
            <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 border-t border-line-soft pt-4">
              <dt class="text-muted">{{ t('home.expires') }}</dt>
              <dd class="text-right tabular">{{ expires }}</dd>
              <dt class="text-muted">{{ t('home.devices') }}</dt>
              <dd class="text-right tabular">
                {{ sub.device_limit > 0 ? t('home.devicesValue', { online: sub.devices_online, limit: sub.device_limit }) : t('home.devicesUnlimited', { online: sub.devices_online }) }}
              </dd>
            </dl>
          </Card>

          <section aria-labelledby="import-h" class="flex flex-col gap-3">
            <div class="flex flex-col gap-0.5">
              <h2 id="import-h" class="text-lg font-semibold">{{ t('home.import') }}</h2>
              <p class="text-muted">{{ t('home.importLead') }}</p>
            </div>
            <div class="grid grid-cols-2 gap-2.5 md:grid-cols-4">
              <template v-for="c in clients" :key="c.name">
                <a
                  v-if="c.href"
                  :href="c.href(sub.subscription_url, serviceName || sub.email)"
                  class="flex flex-col gap-0.5 rounded-[10px] border border-line bg-card px-3.5 py-3 text-ink no-underline hover:border-accent"
                >
                  <span class="font-medium">{{ c.name }}</span>
                  <span class="text-[13px] text-muted">{{ c.platforms }}</span>
                </a>
                <button
                  v-else
                  type="button"
                  class="flex flex-col gap-0.5 rounded-[10px] border border-line bg-card px-3.5 py-3 text-left hover:border-accent"
                  @click="copyFor(c.name)"
                >
                  <span class="font-medium">{{ c.name }}</span>
                  <span class="text-[13px] text-muted">{{ c.platforms }}，复制后添加</span>
                </button>
              </template>
            </div>
          </section>

          <section aria-labelledby="link-h" class="flex flex-col gap-3">
            <h2 id="link-h" class="text-lg font-semibold">{{ t('home.link') }}</h2>
            <SubscriptionLink :url="sub.subscription_url" />
          </section>
        </div>

        <div class="flex min-w-0 flex-[2_1_300px] flex-col gap-7">
          <section aria-labelledby="nodes-h" class="flex flex-col gap-3">
            <h2 id="nodes-h" class="text-lg font-semibold">{{ t('home.nodes') }}</h2>
            <Card as="ul" class="divide-y divide-line-soft">
              <li v-for="n in sub.nodes" :key="n.name" class="flex items-center justify-between gap-3 px-4 py-3">
                <span class="font-medium">{{ n.name }}</span>
                <StatusDot :state="n.status === '正常' ? 'online' : 'maintenance'" :label="n.status" />
              </li>
              <li v-if="sub.nodes.length === 0" class="px-4 py-3 text-muted">{{ t('home.noNodes') }}</li>
            </Card>
          </section>

          <section v-if="usage.data.value" aria-labelledby="usage-h" class="flex flex-col gap-3">
            <div class="flex items-baseline justify-between gap-2">
              <h2 id="usage-h" class="text-lg font-semibold">{{ t('usage.title') }}</h2>
              <span class="text-[13px] text-muted tabular">{{ t('usage.total', { amount: fmt.bytes(usageTotal) }) }}</span>
            </div>
            <Card class="p-4">
              <div class="flex h-20 items-end gap-[2px]" role="img" :aria-label="t('usage.title')">
                <div
                  v-for="d in usage.data.value.items"
                  :key="d.day"
                  class="flex-1 rounded-t-[4px] bg-accent"
                  :style="{ height: d.bytes > 0 ? `max(2px, ${(d.bytes / usageMax) * 100}%)` : '0' }"
                  :title="`${d.day}：${fmt.bytes(d.bytes)}`"
                />
              </div>
            </Card>
          </section>

          <RulePicker />

          <Card v-if="sub.destination_stats" as="section" aria-labelledby="dest-h" class="flex flex-col gap-2 p-4">
            <h2 id="dest-h" class="font-semibold">{{ t('home.destStats') }}</h2>
            <p class="text-[13px] text-muted">{{ t('home.destStatsLead', { days: sub.destination_stats.retention_days }) }}</p>
          </Card>

          <Card as="section" aria-labelledby="reset-h" class="flex flex-col gap-2 p-4">
            <h2 id="reset-h" class="font-semibold">{{ t('home.leaked') }}</h2>
            <p class="text-[13px] text-muted">{{ t('home.leakedLead') }}</p>
            <RouterLink :to="{ name: 'reset-link' }" class="font-medium">{{ t('home.reset') }}</RouterLink>
          </Card>
        </div>
      </div>
    </LoadState>
  </AppShell>
</template>
