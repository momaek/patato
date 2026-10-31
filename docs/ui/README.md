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

- 颜色：底色 `#F4F6F5`，卡片 `#FFFFFF`，正文 `#18201D`，次要文字 `#56625C`，边线 `#D9DFDB`，强调色 `#2B44C4`。
- 状态色：正常 `#1C6B45` / `#E3F2EA`，敏感或待审批 `#A34A06` / `#FBEFE4`，离线或错误 `#B42318` / `#FBE9E7`，待接入 `#4A5560`。状态除颜色外始终带文字。
- 字体：IBM Plex Sans + Noto Sans SC；IBM Plex Mono 只用于 change_id、命令等代码。
- 用户侧展示运营方自己的品牌，服务名、域名、价格、支付方式等未定内容用方括号占位。
