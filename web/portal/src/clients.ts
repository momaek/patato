// 一键导入：打开客户端的 URL scheme，把订阅链接交给它。客户端按 User-Agent 拿到各自的格式。
export interface Client {
  name: string
  platforms: string
  href?: (url: string, name: string) => string
}

const enc = encodeURIComponent

export const clients: Client[] = [
  { name: 'Clash Verge', platforms: 'Windows、macOS', href: (u, n) => `clash://install-config?url=${enc(u)}&name=${enc(n)}` },
  { name: 'Shadowrocket', platforms: 'iPhone、iPad', href: (u, n) => `shadowrocket://add/sub://${btoa(u)}?remark=${enc(n)}` },
  { name: 'sing-box', platforms: 'iOS、Android', href: (u, n) => `sing-box://import-remote-profile?url=${enc(u)}#${enc(n)}` },
  // v2rayN 没有导入用的 URL scheme：复制链接后在「订阅分组」里添加
  { name: 'v2rayN', platforms: 'Windows' },
]
