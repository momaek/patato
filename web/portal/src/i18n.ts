import { createI18n } from 'vue-i18n'
import zhCN from './locales/zh-CN'

// 先只做中文；文案都放在 locales 里，加英文只需要新增一份消息。
export const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})
