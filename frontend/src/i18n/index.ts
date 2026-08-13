import { createI18n } from 'vue-i18n'
import en from './locales/en.json'
import zh from './locales/zh.json'

// 注意：@intlify/vite-plugin-vue-i18n 会在构建期把上述 JSON 编译为
// message functions（非运行时 new Function），因此 runtimeOnly 模式下
// 不再触发 CSP 的 unsafe-eval 限制。

// 默认跟随浏览器语言：含 'zh' 用中文，否则英文
function detectLocale(): 'zh' | 'en' {
  const saved = localStorage.getItem('tlog_locale')
  if (saved === 'zh' || saved === 'en') return saved
  const nav = navigator.language || 'zh'
  return nav.toLowerCase().includes('zh') ? 'zh' : 'en'
}

export const i18n = createI18n({
  legacy: false, // 用 Composition API 模式（<script setup> 配合 useI18n）
  runtimeOnly: true, // 仅运行时，messages 已由插件预编译，避免 unsafe-eval
  locale: detectLocale(),
  fallbackLocale: 'en',
  messages: { zh, en },
})

export function setLocale(loc: 'zh' | 'en') {
  i18n.global.locale.value = loc
  localStorage.setItem('tlog_locale', loc)
}
