<script setup lang="ts">
// 表单项：标签、输入框、提示或错误。错误通过 aria-describedby 关联到输入框。
import { useId } from 'vue'
const props = defineProps<{ label: string; hint?: string; error?: string; type?: string; autocomplete?: string; required?: boolean; placeholder?: string; inputmode?: 'email' | 'text' | 'numeric' }>()
const model = defineModel<string>({ default: '' })
const id = useId()
void props
</script>

<template>
  <div class="flex flex-col gap-1.5">
    <label :for="id" class="font-medium">{{ label }}</label>
    <input
      :id="id"
      v-model="model"
      :type="type ?? 'text'"
      :autocomplete="autocomplete"
      :required="required"
      :placeholder="placeholder"
      :inputmode="inputmode"
      :aria-invalid="error ? true : undefined"
      :aria-describedby="error || hint ? id + '-d' : undefined"
      :class="[
        'min-h-11 rounded-lg border bg-card px-3 text-[15px] outline-none placeholder:text-muted/70 focus:border-accent focus:ring-2 focus:ring-accent/20',
        error ? 'border-bad' : 'border-control',
      ]"
    />
    <p v-if="error" :id="id + '-d'" class="text-[13px] text-bad">{{ error }}</p>
    <p v-else-if="hint" :id="id + '-d'" class="text-[13px] text-muted">{{ hint }}</p>
  </div>
</template>
