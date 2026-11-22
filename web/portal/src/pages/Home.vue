<script setup lang="ts">
import { computed, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { CalendarClock, ChevronRight, Copy, Gauge, Info, KeyRound, Laptop, MonitorSmartphone, Server, Smartphone, Sparkles } from 'lucide-vue-next'
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

const pct = computed(() => {
  const s = sub.value
  return s && s.transfer_bytes > 0 ? Math.min(100, Math.round((s.used_bytes / s.transfer_bytes) * 100)) : 0
})

const resetText = computed(() => {
  const s = sub.value
  if (!s) return ''
  return s.next_reset_at ? t('home.resetOn', { date: fmt.monthDay(s.next_reset_at) }) : t('home.noReset')
})

const expiresMain = computed(() => {
  const s = sub.value
  if (!s?.expires_at) return t('home.never')
  return s.days_left ? t('home.daysLeft', { days: s.days_left }) : t('home.today')
})
const expiresSub = computed(() => (sub.value?.expires_at ? fmt.date(sub.value.expires_at) : t('home.neverSub')))

const devicesMain = computed(() => {
  const s = sub.value
  if (!s) return ''
  return s.device_limit > 0 ? `${s.devices_online} / ${s.device_limit}` : String(s.devices_online)
})
const devicesSub = computed(() => (sub.value && sub.value.device_limit > 0 ? t('home.devicesOnline') : t('home.devicesUnlimitedShort')))

const stats = computed<{ icon: Component; label: string; value: string; sub: string }[]>(() =>
  sub.value
    ? [
        { icon: Gauge, label: t('home.left'), value: fmt.bytes(sub.value.remaining_bytes), sub: resetText.value },
        { icon: CalendarClock, label: t('home.expires'), value: expiresMain.value, sub: expiresSub.value },
        { icon: MonitorSmartphone, label: t('home.devices'), value: devicesMain.value, sub: devicesSub.value },
      ]
    : [],
)

const clientIcon = (platforms: string) => (/iPhone|iOS|Android/.test(platforms) ? Smartphone : Laptop)

function copyFor(name: string) {
  if (!sub.value) return
  navigator.clipboard.writeText(sub.value.subscription_url).then(
    () => toast.success(t('home.copiedFor', { name })),
    (e) => toast.error(errorText(e)),
  )
}
</script>

<template>
  <AppShell :service-name="serviceName" wide>
    <LoadState :loading="isPending" :error="error" :message="error ? errorText(error) : undefined" @retry="refetch()">
      <div v-if="sub" class="flex flex-col gap-6">
        <!-- 订阅概览 -->
        <Card as="section" aria-labelledby="plan-h" class="relative overflow-hidden">
          <div aria-hidden="true" class="pointer-events-none absolute inset-x-0 top-0 h-40 bg-gradient-to-b from-accent-soft to-transparent opacity-70" />
          <div class="relative flex flex-col gap-6 p-5 md:p-7">
            <div class="flex flex-wrap items-start justify-between gap-4">
              <div class="flex min-w-0 flex-col gap-2">
                <span class="text-[13px] font-medium text-muted">{{ t('home.myPlan') }}</span>
                <div class="flex flex-wrap items-center gap-3">
                  <h1 id="plan-h" class="text-[26px] leading-tight font-bold tracking-tight md:text-[30px]">{{ sub.plan?.name ?? t('home.noPlan') }}</h1>
                  <Badge :tone="stateTone[sub.state]" class="rounded-full px-2.5 py-1">
                    <span aria-hidden="true" class="mr-1.5 size-1.5 rounded-full bg-current" />{{ t(`home.state.${sub.state}`) }}
                  </Badge>
                </div>
              </div>
              <RouterLink v-if="config.data.value?.payment" :to="{ name: 'buy' }" class="btn-primary max-sm:w-full">
                <Sparkles class="size-4" aria-hidden="true" />{{ sub.plan ? t('home.renew') : t('home.buyPlan') }}
              </RouterLink>
            </div>

            <Alert v-if="sub.state !== 'active'" :tone="sub.state === 'over_quota' ? 'warn' : 'bad'">{{ t(`home.stateHint.${sub.state}`) }}</Alert>

            <div class="flex flex-col gap-2.5">
              <div class="flex flex-wrap items-end justify-between gap-2">
                <span class="font-medium">{{ t('home.traffic') }}</span>
                <span class="tabular">
                  <strong class="text-[22px] font-bold tracking-tight">{{ fmt.bytes(sub.used_bytes) }}</strong>
                  <span class="ml-1 text-muted">{{ t('home.trafficOf', { total: fmt.bytes(sub.transfer_bytes) }) }}</span>
                </span>
              </div>
              <Progress
                class="h-2.5!"
                :value="sub.used_bytes"
                :max="sub.transfer_bytes"
                :label="t('home.traffic')"
                :value-text="`${fmt.bytes(sub.used_bytes)} / ${fmt.bytes(sub.transfer_bytes)}`"
              />
              <span class="text-[13px] text-muted tabular">{{ t('home.usedPct', { pct }) }}</span>
            </div>

            <dl class="grid gap-3 sm:grid-cols-3">
              <div v-for="s in stats" :key="s.label" class="flex items-center gap-3 rounded-control border border-line-soft bg-bg/60 p-3.5">
                <span class="icon-tile"><component :is="s.icon" class="size-5" aria-hidden="true" /></span>
                <div class="flex min-w-0 flex-col">
                  <dt class="text-[13px] text-muted">{{ s.label }}</dt>
                  <dd class="truncate text-[17px] font-semibold tabular">{{ s.value }}</dd>
                  <dd class="truncate text-[12px] text-muted tabular">{{ s.sub }}</dd>
                </div>
              </div>
            </dl>
          </div>
        </Card>

        <div class="grid items-start gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
          <div class="flex min-w-0 flex-col gap-6">
            <!-- 一键导入 -->
            <Card as="section" aria-labelledby="import-h" class="flex flex-col gap-4 p-5">
              <div class="flex flex-col gap-0.5">
                <h2 id="import-h" class="text-lg font-semibold">{{ t('home.import') }}</h2>
                <p class="text-[14px] text-muted">{{ t('home.importLead') }}</p>
              </div>
              <div class="grid gap-2.5 sm:grid-cols-2">
                <template v-for="c in clients" :key="c.name">
                  <component
                    :is="c.href ? 'a' : 'button'"
                    :href="c.href ? c.href(sub.subscription_url, serviceName || sub.email) : undefined"
                    :type="c.href ? undefined : 'button'"
                    class="group flex min-h-16 items-center gap-3 rounded-control border border-line bg-card px-3.5 py-3 text-left text-ink no-underline transition-colors duration-150 hover:border-accent hover:bg-accent-soft/40 hover:text-ink"
                    @click="c.href ? undefined : copyFor(c.name)"
                  >
                    <span class="icon-tile"><component :is="clientIcon(c.platforms)" class="size-5" aria-hidden="true" /></span>
                    <span class="flex min-w-0 flex-1 flex-col">
                      <span class="font-semibold">{{ c.name }}</span>
                      <span class="truncate text-[13px] text-muted">{{ c.href ? c.platforms : t('home.copyThenAdd', { platforms: c.platforms }) }}</span>
                    </span>
                    <component :is="c.href ? ChevronRight : Copy" class="size-4 shrink-0 text-muted transition-transform duration-150 group-hover:text-accent" aria-hidden="true" />
                  </component>
                </template>
              </div>
            </Card>

            <!-- 订阅链接 -->
            <Card as="section" aria-labelledby="link-h" class="flex flex-col gap-4 p-5">
              <div class="flex flex-col gap-0.5">
                <h2 id="link-h" class="text-lg font-semibold">{{ t('home.link') }}</h2>
                <p class="text-[14px] text-muted">{{ t('home.linkLead') }}</p>
              </div>
              <SubscriptionLink :url="sub.subscription_url" />
            </Card>

            <!-- 用量 -->
            <Card v-if="usage.data.value" as="section" aria-labelledby="usage-h" class="flex flex-col gap-4 p-5">
              <div class="flex items-baseline justify-between gap-2">
                <h2 id="usage-h" class="text-lg font-semibold">{{ t('usage.title') }}</h2>
                <span class="text-[13px] text-muted tabular">{{ t('usage.total', { amount: fmt.bytes(usageTotal) }) }}</span>
              </div>
              <div class="flex h-28 items-end gap-[3px]" role="img" :aria-label="t('usage.aria', { amount: fmt.bytes(usageTotal) })">
                <div v-for="d in usage.data.value.items" :key="d.day" class="group relative flex h-full flex-1 items-end">
                  <div
                    class="w-full rounded-t-[3px] bg-accent/80 transition-colors duration-150 group-hover:bg-accent"
                    :style="{ height: d.bytes > 0 ? `max(3px, ${(d.bytes / usageMax) * 100}%)` : '2px', opacity: d.bytes > 0 ? 1 : 0.25 }"
                    :title="`${d.day}：${fmt.bytes(d.bytes)}`"
                  />
                </div>
              </div>
              <div v-if="usage.data.value.items.length" class="-mt-2 flex justify-between text-[12px] text-muted tabular">
                <span>{{ usage.data.value.items[0]!.day.slice(5) }}</span>
                <span>{{ usage.data.value.items[usage.data.value.items.length - 1]!.day.slice(5) }}</span>
              </div>
            </Card>
          </div>

          <div class="flex min-w-0 flex-col gap-6">
            <!-- 节点 -->
            <Card as="section" aria-labelledby="nodes-h" class="flex flex-col">
              <div class="flex items-baseline justify-between gap-2 px-5 pt-5 pb-3">
                <h2 id="nodes-h" class="text-lg font-semibold">{{ t('home.nodes') }}</h2>
                <span class="text-[13px] text-muted tabular">{{ t('home.nodesCount', { n: sub.nodes.length }) }}</span>
              </div>
              <ul class="flex max-h-[360px] flex-col overflow-y-auto px-2 pb-2">
                <li v-for="n in sub.nodes" :key="n.name" class="flex items-center gap-3 rounded-control px-3 py-2.5">
                  <Server class="size-4 shrink-0 text-muted" aria-hidden="true" />
                  <span class="min-w-0 flex-1 truncate font-medium">{{ n.name }}</span>
                  <StatusDot :state="n.status === '正常' ? 'online' : 'maintenance'" :label="n.status" class="text-[13px]" />
                </li>
                <li v-if="sub.nodes.length === 0" class="px-3 py-3 text-muted">{{ t('home.noNodes') }}</li>
              </ul>
            </Card>

            <RulePicker />

            <!-- 链接泄露 -->
            <Card as="section" aria-labelledby="reset-h" class="flex items-start gap-3.5 p-5">
              <span class="icon-tile bg-warn-soft text-warn"><KeyRound class="size-5" aria-hidden="true" /></span>
              <div class="flex min-w-0 flex-col gap-1">
                <h2 id="reset-h" class="font-semibold">{{ t('home.leaked') }}</h2>
                <p class="text-[14px] text-muted">{{ t('home.leakedLead') }}</p>
                <RouterLink :to="{ name: 'reset-link' }" class="mt-1 inline-flex min-h-10 items-center gap-1 self-start font-medium">
                  {{ t('home.reset') }}<ChevronRight class="size-4" aria-hidden="true" />
                </RouterLink>
              </div>
            </Card>

            <Card v-if="sub.destination_stats" as="section" aria-labelledby="dest-h" class="flex items-start gap-3.5 p-5">
              <span class="icon-tile bg-idle-soft text-idle"><Info class="size-5" aria-hidden="true" /></span>
              <div class="flex min-w-0 flex-col gap-1">
                <h2 id="dest-h" class="font-semibold">{{ t('home.destStats') }}</h2>
                <p class="text-[13px] text-muted">{{ t('home.destStatsLead', { days: sub.destination_stats.retention_days }) }}</p>
              </div>
            </Card>
          </div>
        </div>
      </div>
    </LoadState>
  </AppShell>
</template>
