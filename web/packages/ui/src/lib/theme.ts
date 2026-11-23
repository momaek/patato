// 深浅色：默认跟随系统，手动选择后记在 localStorage（Admin 与用户侧共用同一个键）。
// 实际生效的颜色写到 <html data-theme>，各 app 的 main.css 按它覆盖 token。
// 各 app 的 index.html 里有同样逻辑的内联脚本（首帧前执行，避免闪白），改这里的键或属性时要一起改。
import { ref, watch } from 'vue'

export type ThemePref = 'system' | 'light' | 'dark'

export interface ThemeOptions {
  /** 同步 <meta name="theme-color"> 的颜色；省略则不处理。 */
  themeColor?: { light: string; dark: string }
}

const KEY = 'patato.theme'

function read(): ThemePref {
  const v = localStorage.getItem(KEY)
  return v === 'light' || v === 'dark' ? v : 'system'
}

export const themePref = ref<ThemePref>('system')
export const isDark = ref(false)

let started = false

/** 在 app 入口调用一次：读取已保存的选择、应用到 <html>，并跟随之后的切换与系统变化。 */
export function initTheme(options: ThemeOptions = {}) {
  if (started) return
  started = true

  const media = window.matchMedia('(prefers-color-scheme: dark)')
  const apply = () => {
    isDark.value = themePref.value === 'dark' || (themePref.value === 'system' && media.matches)
    document.documentElement.dataset.theme = isDark.value ? 'dark' : 'light'
    if (options.themeColor) {
      document
        .querySelector('meta[name="theme-color"]')
        ?.setAttribute('content', isDark.value ? options.themeColor.dark : options.themeColor.light)
    }
  }

  themePref.value = read()
  watch(themePref, (v) => {
    if (v === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, v)
    apply()
  })
  media.addEventListener('change', apply)
  apply()
}

/** 依次切换：跟随系统 → 浅色 → 深色。 */
export function cycleTheme() {
  themePref.value = themePref.value === 'system' ? 'light' : themePref.value === 'light' ? 'dark' : 'system'
}
