<script setup lang="ts">
// 分流规则：用户从管理员允许自选的规则里选一个，或跟随套餐默认。客户端下次更新订阅时生效。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryClient } from '@tanstack/vue-query'
import { toast } from 'vue-sonner'
import { api, errorText, type MyRuleProfiles } from '@patato/api'
import { Button, Card } from '@patato/ui'
import { Split } from 'lucide-vue-next'
import { useAuthed } from '../session'

const { t } = useI18n()
const qc = useQueryClient()
const q = useAuthed(['me', 'rule-profiles'], () => api.get<MyRuleProfiles>('/api/v1/me/rule-profiles'))
const choice = ref('')
watch(() => q.data.value?.selected, (v) => (choice.value = v ?? ''), { immediate: true })

const followName = computed(() => q.data.value?.plan_default?.name ?? t('rules.builtin'))
const picked = computed(() => (choice.value ? q.data.value?.items.find((i) => i.code === choice.value) : q.data.value?.plan_default))
const dirty = computed(() => choice.value !== (q.data.value?.selected ?? ''))
const busy = ref(false)

async function save() {
  busy.value = true
  try {
    await api.put('/api/v1/me/rule-profile', { body: { rule_profile: choice.value } })
    await qc.invalidateQueries({ queryKey: ['me'] })
    toast.success(t('rules.saved'))
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Card v-if="q.data.value && (q.data.value.items.length || q.data.value.effective)" as="section" aria-labelledby="rules-h" class="flex flex-col gap-3 p-5">
    <div class="flex items-start gap-3.5">
      <span class="icon-tile"><Split class="size-5" aria-hidden="true" /></span>
      <div class="flex min-w-0 flex-col gap-0.5">
        <h2 id="rules-h" class="font-semibold">{{ t('rules.title') }}</h2>
        <p class="text-[13px] text-muted">{{ t('rules.lead') }}</p>
      </div>
    </div>
    <template v-if="q.data.value.items.length">
      <label class="flex flex-col gap-1.5">
        <span class="sr-only">{{ t('rules.title') }}</span>
        <select v-model="choice" class="field-input">
          <option value="">{{ t('rules.follow', { name: followName }) }}</option>
          <option v-for="r in q.data.value.items" :key="r.code" :value="r.code">{{ r.name }}</option>
        </select>
      </label>
      <p v-if="picked?.description" class="text-[13px] text-muted">{{ picked.description }}</p>
      <p v-if="picked?.groups.length" class="text-[13px] text-muted">{{ t('rules.groups', { groups: picked.groups.join('、') }) }}</p>
      <div v-if="dirty" class="flex flex-col gap-2">
        <Button variant="primary" class="self-start" :disabled="busy" @click="save">{{ t('rules.save') }}</Button>
        <p class="text-[13px] text-muted">{{ t('rules.hint') }}</p>
      </div>
    </template>
    <p v-else class="text-[13px]">{{ t('rules.current', { name: q.data.value.effective?.name }) }}</p>
  </Card>
</template>
