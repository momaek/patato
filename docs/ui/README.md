# Patato 界面设计稿

在线画布：https://claude.ai/artifact/UQ65VDBo4Q92jax77BtLVf

本目录是画布的源文件，用于留档和版本对比。`.dc.html` 依赖画布运行时，直接用浏览器打开无法正常显示；查看和修改请到在线画布。`canvas.json` 记录每个页面在画布上的位置和旁边的 API 便签。

Admin 与用户侧是两个独立的前端应用（Vue 3 + shadcn-vue），只通过 `/api/v1` 与后端通信。每个页面调用的接口写在画布上该页旁边的蓝色便签里。

## Admin（运营者）

| 页面 | 文件 | 阶段 |
| --- | --- | --- |
| 总览 | `Main.dc.html` | MVP |
| 审批详情 | `Approval.dc.html` | MVP |
| 节点 | `Nodes.dc.html` | MVP |
| 审计日志 | `Audit.dc.html` | MVP |
| 套餐 | `Plans.dc.html` | MVP |
| 用户 | `Users.dc.html` | MVP |
| 订单 | `Orders.dc.html` | 二期 |
| 工单 | `Tickets.dc.html` | 二期 |
| 返佣 | `Referrals.dc.html` | 二期 |

## 用户侧（终端用户）

| 页面 | 文件 | 阶段 |
| --- | --- | --- |
| 登录 | `UserLogin.dc.html` | MVP |
| 我的订阅 | `UserHome.dc.html` | MVP |
| 我的订阅（宽屏） | `UserHomeDesktop.dc.html` | MVP |
| 找回密码 | `UserForgot.dc.html` | MVP |
| 设置新密码 | `UserNewPassword.dc.html` | MVP |
| 重置订阅链接 | `UserResetLink.dc.html` | MVP |
| 注册 | `UserRegister.dc.html` | 待定 |
| 续费与购买 | `UserPlans.dc.html` | 待定 |
| 支付 | `UserCheckout.dc.html` | 二期 |
| 工单 | `UserTickets.dc.html` | 二期 |
| 工单详情 | `UserTicketDetail.dc.html` | 二期 |
| 邀请返佣 | `UserInvite.dc.html` | 二期 |

## 设计约定

2026-11 按 [ui-ux-pro-max](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill) 的推荐重做了视觉层（`--design-system "admin dashboard network infrastructure operations console" --density 8`）：风格为 Minimalism & Swiss Style（高密度仪表盘），配色取「Dark tech + status green」。上面画布里的稿子是改版前的版式参考，颜色和字体以这里为准。

- 主题：浅色 / 深色，默认跟随系统，Admin 侧栏底部可切换（`localStorage` 键 `patato.theme`，与用户侧共用）。生效的颜色写在 `<html data-theme>`，见 `web/packages/ui/src/lib/theme.ts` 与两个 app 的 `index.html` 内联脚本。
- 颜色：组件只用语义 token（`bg-bg`、`bg-card`、`text-ink`、`text-muted`、`border-line`、`bg-accent` + `text-on-accent` …），不写原始色值。`web/packages/ui/src/styles.css` 里的默认值即 Admin 浅色；Admin 深色在 `web/admin/src/main.css`，用户侧的蓝色主题在 `web/portal/src/main.css` 覆盖。
  - 浅色：底 `#F6F8FA`，卡片 `#FFFFFF`，正文 `#0F172A`，次要 `#475569`，边线 `#E2E8F0`，强调 `#15803D`。
  - 深色：底 `#0B1120`，卡片 `#131B2C`，正文 `#F1F5F9`，次要 `#94A3B8`，边线 `#253045`，强调 `#22C55E`（按钮文字 `#052E16`）。
- 状态色（浅 / 深）：正常 `#15803D` / `#4ADE80`，敏感或待审批 `#B45309` / `#FBBF24`，离线或错误 `#B91C1C` / `#F87171`，待接入 `#475569` / `#94A3B8`。状态除颜色外始终带文字。文字与底色组合满足 WCAG AA 4.5:1。
- 字体：Admin 用 Fira Sans（正文）+ Fira Code（change_id、命令等代码），用户侧正文用 Plus Jakarta Sans；中文回落到系统字体，不从外网加载。
- 图标：lucide-vue-next，不用 emoji；装饰图标 `aria-hidden`，纯图标按钮必须有 `aria-label`。
- 交互：可点击元素有 pointer 光标和 150ms 左右的过渡，焦点环始终可见，尊重 `prefers-reduced-motion`。Admin 的分段筛选统一用 `seg-tabs` / `seg-tab` 工具类，原生下拉框和多行文本用 `field-input`。
- 用户侧展示运营方自己的品牌，服务名、域名、价格、支付方式等未定内容用方括号占位。
