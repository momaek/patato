// 深浅色：默认跟随系统，用户手动选择后记在 localStorage。
// 实际生效的颜色写到 <html data-theme>；index.html 里有同样逻辑的内联脚本，避免首屏闪白。
import { ref, watch } from 'vue'

export type ThemePref = 'system' | 'light' | 'dark'
const KEY = 'patato.theme'
const media = window.matchMedia('(prefers-color-scheme: dark)')

function read(): ThemePref {
  const v = localStorage.getItem(KEY)
  return v === 'light' || v === 'dark' ? v : 'system'
}

export const themePref = ref<ThemePref>(read())

function apply() {
  const dark = themePref.value === 'dark' || (themePref.value === 'system' && media.matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', dark ? '#0b1120' : '#f5f7fb')
}

watch(themePref, (v) => {
  if (v === 'system') localStorage.removeItem(KEY)
  else localStorage.setItem(KEY, v)
  apply()
})
media.addEventListener('change', apply)
apply()

/** 依次切换：跟随系统 → 浅色 → 深色。 */
export function cycleTheme() {
  themePref.value = themePref.value === 'system' ? 'light' : themePref.value === 'light' ? 'dark' : 'system'
}
