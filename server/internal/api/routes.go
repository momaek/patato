package api

import (
	"github.com/gin-gonic/gin"
)

// 接口的访问类别
const (
	accessPublic   = "public"   // 无需登录
	accessAdmin    = "admin"    // 运营者会话或 CLI / Agent token
	accessOperator = "operator" // 只接受运营者网页会话
	accessPortal   = "portal"   // 终端用户会话
	accessMachine  = "machine"  // patato-node 的机器 token
)

// route 是一条对外接口。Path 与 api/openapi.yaml 一致，Risk 对应契约里的 x-risk：
// 只读接口为 read；写接口声明的是最低等级，实际等级由变更计划按影响面决定。
type route struct {
	Method  string
	Path    string
	Access  string
	Risk    string
	Op      string // 对应的变更操作，写接口才有
	Summary string
	Handler gin.HandlerFunc
}

func (r route) chain(s *Server) []gin.HandlerFunc {
	var hs []gin.HandlerFunc
	switch r.Access {
	case accessAdmin:
		hs = append(hs, s.adminAuth)
		if r.Risk != "read" {
			hs = append(hs, writeScope)
			if r.Method != "GET" {
				hs = append(hs, s.idempotent)
			}
		}
	case accessOperator:
		hs = append(hs, s.adminAuth, operatorOnly)
	case accessPortal:
		hs = append(hs, s.portalAuth)
	case accessMachine:
		hs = append(hs, s.d.Native.Auth)
	}
	return append(hs, r.Handler)
}

// draftExtra 给工单草稿填上工单号和草拟人（哪个 token、自报的哪个会话），请求里的值会被覆盖。
func (s *Server) draftExtra(c *gin.Context) map[string]any {
	p := getPrincipal(c)
	label := p.actor().Label
	if p.claim != "" {
		label += "（" + p.claim + "）"
	}
	return map[string]any{"ticket": c.Param("id"), "drafted_by": label}
}

func (s *Server) routes() []route {
	return []route{
		// 系统与说明书
		{"GET", "/api/v1/tools", accessPublic, "read", "", "Agent 工具清单（与 /skill.md 同源）", s.tools},
		{"GET", "/api/v1/openapi.yaml", accessPublic, "read", "", "API 契约", s.openapi},

		// 运营者登录与凭证
		{"POST", "/api/v1/auth/passkey/register:begin", accessPublic, "normal", "", "开始注册 passkey（凭邀请）", s.passkeyRegisterBegin},
		{"POST", "/api/v1/auth/passkey/register:finish", accessPublic, "normal", "", "完成注册 passkey", s.passkeyRegisterFinish},
		{"POST", "/api/v1/auth/passkey/login:begin", accessPublic, "normal", "", "开始 passkey 登录", s.passkeyLoginBegin},
		{"POST", "/api/v1/auth/passkey/login:finish", accessPublic, "normal", "", "完成 passkey 登录", s.passkeyLoginFinish},
		{"POST", "/api/v1/auth/logout", accessPublic, "normal", "", "退出登录；带 Bearer token 时吊销这个 token", s.logout},
		{"GET", "/api/v1/auth/me", accessAdmin, "read", "", "当前调用方", s.whoami},
		{"POST", "/api/v1/auth/passkeys:begin", accessOperator, "normal", "", "再添加一把 passkey", s.passkeyAddBegin},
		{"POST", "/api/v1/auth/passkeys:finish", accessOperator, "normal", "", "完成添加 passkey", s.passkeyAddFinish},
		{"POST", "/api/v1/auth/cli/grants", accessPublic, "normal", "", "CLI 发起授权", s.cliGrantCreate},
		{"POST", "/api/v1/auth/cli/grants/{id}:token", accessPublic, "normal", "", "CLI 轮询换取 token", s.cliGrantToken},
		{"GET", "/api/v1/auth/cli/grants/{id}", accessOperator, "read", "", "查看授权请求", s.cliGrantShow},
		{"POST", "/api/v1/auth/cli/grants/{id}:approve", accessOperator, "normal", "", "批准 CLI 授权", s.cliGrantApprove},
		{"POST", "/api/v1/auth/cli/grants/{id}:deny", accessOperator, "normal", "", "拒绝 CLI 授权", s.cliGrantDeny},
		{"GET", "/api/v1/auth/tokens", accessOperator, "read", "", "已签发的 token", s.tokenList},
		{"DELETE", "/api/v1/auth/tokens/{id}", accessOperator, "normal", "", "吊销 token", s.tokenRevoke},
		{"POST", "/api/v1/auth/telegram:bind", accessOperator, "normal", "", "生成 Telegram 绑定码", s.telegramBind},

		// 总览、事件、审计
		{"GET", "/api/v1/status", accessAdmin, "read", "", "系统总览：需要处理的事、节点、用户、用量", s.status},
		{"GET", "/api/v1/events", accessAdmin, "read", "", "事件列表；Accept: text/event-stream 时持续推送", s.events},
		{"GET", "/api/v1/audit", accessAdmin, "read", "", "审计日志", s.auditList},
		{"GET", "/api/v1/stats/usage", accessAdmin, "read", "", "今日用量", s.usage},
		{"GET", "/api/v1/audit/export", accessAdmin, "read", "", "导出审计日志（CSV 或 JSON，支持按时间、动作筛选）", s.auditExport},

		// 报表
		{"GET", "/api/v1/reports/traffic", accessAdmin, "read", "", "流量报表：每日流量与用量最多的用户", s.reportTraffic},
		{"GET", "/api/v1/reports/revenue", accessAdmin, "read", "", "收入报表：每日收入、退款、按套餐汇总", s.reportRevenue},
		{"GET", "/api/v1/reports/nodes", accessAdmin, "read", "", "节点成本报表：流量、成本与每 GB 成本", s.reportNodes},

		// 节点模板
		{"GET", "/api/v1/templates", accessAdmin, "read", "", "节点模板列表", s.templateList},
		{"GET", "/api/v1/templates/{id}", accessAdmin, "read", "", "节点模板详情", s.templateGet},
		{"POST", "/api/v1/templates", accessAdmin, "normal", "template.create", "新建节点模板", s.mutate("template.create", nil)},
		{"PATCH", "/api/v1/templates/{id}", accessAdmin, "normal", "template.update", "修改节点模板", s.mutate("template.update", pathRef("template"))},

		// 节点
		{"GET", "/api/v1/nodes", accessAdmin, "read", "", "节点列表", s.nodeList},
		{"GET", "/api/v1/nodes/{id}", accessAdmin, "read", "", "节点详情", s.nodeGet},
		{"GET", "/api/v1/nodes/{id}/install-cmd", accessAdmin, "normal", "", "节点后端安装命令（含节点密钥，需要可写 token）", s.nodeInstallCmd},
		{"POST", "/api/v1/nodes:provision", accessAdmin, "normal", "node.provision", "意图级上节点：建节点并挂到套餐", s.mutate("node.provision", nil)},
		{"PATCH", "/api/v1/nodes/{id}", accessAdmin, "normal", "node.update", "修改节点", s.mutate("node.update", pathRef("node"))},
		{"POST", "/api/v1/nodes/{id}:attach", accessAdmin, "normal", "node.attach", "把节点挂到套餐", s.mutate("node.attach", pathRef("node"))},
		{"POST", "/api/v1/nodes/{id}:detach", accessAdmin, "normal", "node.detach", "把节点从套餐卸载", s.mutate("node.detach", pathRef("node"))},
		{"POST", "/api/v1/nodes/{id}:rotate-key", accessAdmin, "normal", "node.rotate_key", "轮换节点密钥", s.mutate("node.rotate_key", pathRef("node"))},
		{"DELETE", "/api/v1/nodes/{id}", accessAdmin, "sensitive", "node.delete", "删除节点", s.mutate("node.delete", pathRef("node"))},

		// 机器（patato-node）
		{"GET", "/api/v1/machines", accessAdmin, "read", "", "机器列表：接入状态与承载的节点", s.machineList},
		{"GET", "/api/v1/machines/{id}", accessAdmin, "read", "", "机器详情", s.machineGet},
		{"GET", "/api/v1/machines/{id}/install-cmd", accessAdmin, "normal", "", "patato-node 安装命令（含一次性注册码，需要可写 token）", s.machineInstallCmd},
		{"POST", "/api/v1/machines/{id}:revoke", accessAdmin, "normal", "machine.revoke", "吊销机器的 patato-node 凭证", s.mutate("machine.revoke", pathRef("machine"))},

		// 原生节点协议（patato-node 调用）
		{"POST", "/api/v1/node/enroll", accessPublic, "normal", "", "patato-node 凭注册码换取机器 token", s.nativeHandler("enroll")},
		{"POST", "/api/v1/node/sync", accessMachine, "normal", "", "patato-node 同步节点配置与用户", s.nativeHandler("sync")},
		{"POST", "/api/v1/node/report", accessMachine, "normal", "", "patato-node 上报流量、在线 IP 与运行状态", s.nativeHandler("report")},
		{"GET", "/api/v1/node/ws", accessMachine, "read", "", "patato-node 的推送通道（WebSocket）", s.nativeHandler("ws")},

		// 套餐
		{"GET", "/api/v1/plans", accessAdmin, "read", "", "套餐列表", s.planList},
		{"GET", "/api/v1/plans/{id}", accessAdmin, "read", "", "套餐详情", s.planGet},
		{"POST", "/api/v1/plans", accessAdmin, "normal", "plan.create", "新建套餐（带价格时为敏感）", s.mutate("plan.create", nil)},
		{"PATCH", "/api/v1/plans/{id}", accessAdmin, "normal", "plan.update", "修改套餐（改价格时为敏感）", s.mutate("plan.update", pathRef("plan"))},
		{"DELETE", "/api/v1/plans/{id}", accessAdmin, "sensitive", "plan.delete", "删除套餐", s.mutate("plan.delete", pathRef("plan"))},
		{"GET", "/api/v1/plans/{id}/revisions", accessAdmin, "read", "", "套餐历史版本", s.planRevisions},

		// 分流规则（subconverter 格式的 ini 模板，例如 ACL4SSR）
		{"GET", "/api/v1/rule-profiles", accessAdmin, "read", "", "分流规则列表", s.ruleProfileList},
		{"GET", "/api/v1/rule-profiles/presets", accessAdmin, "read", "", "可一键导入的 ACL4SSR 模板", s.ruleProfilePresets},
		{"GET", "/api/v1/rule-profiles/{id}", accessAdmin, "read", "", "分流规则详情：ini 内容、策略组、规则文件缓存状态", s.ruleProfileGet},
		{"POST", "/api/v1/rule-profiles", accessAdmin, "normal", "rule_profile.create", "新建分流规则（ini 地址或内容，或用 preset 导入内置模板）", s.mutate("rule_profile.create", nil)},
		{"PATCH", "/api/v1/rule-profiles/{id}", accessAdmin, "normal", "rule_profile.update", "修改分流规则", s.mutate("rule_profile.update", pathRef("rule_profile"))},
		{"POST", "/api/v1/rule-profiles/{id}:refresh", accessAdmin, "normal", "rule_profile.refresh", "立即重新拉取分流规则", s.mutate("rule_profile.refresh", pathRef("rule_profile"))},
		{"DELETE", "/api/v1/rule-profiles/{id}", accessAdmin, "normal", "rule_profile.delete", "删除分流规则", s.mutate("rule_profile.delete", pathRef("rule_profile"))},

		// 用户
		{"GET", "/api/v1/users", accessAdmin, "read", "", "用户列表", s.userList},
		{"GET", "/api/v1/users/{id}", accessAdmin, "read", "", "用户详情", s.userGet},
		{"POST", "/api/v1/users", accessAdmin, "normal", "user.create", "新建用户", s.mutate("user.create", nil)},
		{"PATCH", "/api/v1/users/{id}", accessAdmin, "normal", "user.update", "修改用户（换套餐、续期、限速、封禁）", s.mutate("user.update", pathRef("user"))},
		{"POST", "/api/v1/users/{id}:reset-traffic", accessAdmin, "normal", "user.reset_traffic", "重置用户流量", s.mutate("user.reset_traffic", pathRef("user"))},
		{"POST", "/api/v1/users/{id}:reset-token", accessAdmin, "normal", "user.reset_token", "重置用户订阅", s.mutate("user.reset_token", pathRef("user"))},
		{"DELETE", "/api/v1/users/{id}", accessAdmin, "sensitive", "user.delete", "删除用户", s.mutate("user.delete", pathRef("user"))},

		// 订单与支付
		{"GET", "/api/v1/orders", accessAdmin, "read", "", "订单列表", s.orderList},
		{"GET", "/api/v1/orders/{id}", accessAdmin, "read", "", "订单详情：支付回调、返佣、审计", s.orderGet},
		{"POST", "/api/v1/orders/{id}:refund", accessAdmin, "sensitive", "order.refund", "退款：扣回套餐时长、退回余额、撤销返佣", s.mutate("order.refund", pathRef("order"))},
		{"POST", "/api/v1/orders/{id}:mark-paid", accessAdmin, "sensitive", "order.mark_paid", "支付回调丢失时手动确认收款并开通", s.mutate("order.mark_paid", pathRef("order"))},
		{"POST", "/api/v1/orders/{id}:cancel", accessAdmin, "normal", "order.cancel", "取消待支付订单", s.mutate("order.cancel", pathRef("order"))},

		// 优惠券
		{"GET", "/api/v1/coupons", accessAdmin, "read", "", "优惠券列表", s.couponList},
		{"GET", "/api/v1/coupons/{id}", accessAdmin, "read", "", "优惠券详情", s.couponGet},
		{"POST", "/api/v1/coupons", accessAdmin, "sensitive", "coupon.create", "新建优惠券", s.mutate("coupon.create", nil)},
		{"PATCH", "/api/v1/coupons/{id}", accessAdmin, "normal", "coupon.update", "修改优惠券（只停用时为常规，其余为敏感）", s.mutate("coupon.update", pathRef("coupon"))},

		// 邀请返佣
		{"GET", "/api/v1/referrals", accessAdmin, "read", "", "返佣明细与汇总，可按状态（frozen 等）和邀请人筛选", s.referralList},
		{"GET", "/api/v1/referrals/settings", accessAdmin, "read", "", "返佣规则", s.referralSettingsGet},
		{"PUT", "/api/v1/referrals/settings", accessAdmin, "sensitive", "referral.settings", "修改返佣规则", s.mutate("referral.settings", nil)},
		{"POST", "/api/v1/referrals/{id}:release", accessAdmin, "sensitive", "referral.release", "解冻邀请人被冻结的返佣", s.mutate("referral.release", pathRef("user"))},

		// 工单
		{"GET", "/api/v1/tickets", accessAdmin, "read", "", "工单列表", s.ticketList},
		{"GET", "/api/v1/tickets/{id}", accessAdmin, "read", "", "工单详情：对话、草稿，以及用户状态、订单、节点状态", s.ticketGet},
		{"POST", "/api/v1/tickets/{id}:draft", accessAdmin, "normal", "ticket.draft", "保存回复草稿（不会发给用户），或标为「等你处理」", s.mutate("ticket.draft", s.draftExtra)},
		{"POST", "/api/v1/tickets/{id}:reply", accessOperator, "normal", "", "发送回复", s.ticketReply},
		{"POST", "/api/v1/tickets/{id}:close", accessAdmin, "normal", "ticket.close", "关闭工单", s.mutate("ticket.close", pathRef("ticket"))},

		// Webhook 与自动处置
		{"GET", "/api/v1/webhooks", accessAdmin, "read", "", "Webhook 列表", s.webhookList},
		{"POST", "/api/v1/webhooks", accessAdmin, "sensitive", "webhook.create", "新建 Webhook：把事件推送到外部地址", s.mutate("webhook.create", nil)},
		{"DELETE", "/api/v1/webhooks/{id}", accessAdmin, "normal", "webhook.delete", "删除 Webhook", s.mutate("webhook.delete", pathRef("webhook"))},
		{"GET", "/api/v1/webhooks/{id}/secret", accessOperator, "read", "", "Webhook 签名密钥", s.webhookSecret},
		{"GET", "/api/v1/automations", accessAdmin, "read", "", "自动处置规则列表", s.automationList},
		{"POST", "/api/v1/automations", accessAdmin, "sensitive", "automation.create", "新建自动处置规则", s.mutate("automation.create", nil)},
		{"PATCH", "/api/v1/automations/{id}", accessAdmin, "normal", "automation.update", "修改自动处置规则（只停用时为常规，其余为敏感）", s.mutate("automation.update", pathRef("rule"))},
		{"DELETE", "/api/v1/automations/{id}", accessAdmin, "normal", "automation.delete", "删除自动处置规则", s.mutate("automation.delete", pathRef("rule"))},

		// 变更计划
		{"GET", "/api/v1/changes/{id}", accessAdmin, "read", "", "变更计划详情", s.changeGet},
		{"POST", "/api/v1/changes/{id}:apply", accessAdmin, "normal", "", "执行预演好的变更计划（敏感计划转为审批）", s.changeApply},

		// 审批
		{"GET", "/api/v1/approvals", accessAdmin, "read", "", "审批列表", s.approvalList},
		{"GET", "/api/v1/approvals/{id}", accessAdmin, "read", "", "审批详情", s.approvalGet},
		{"POST", "/api/v1/approvals/{id}:approve-begin", accessOperator, "sensitive", "", "开始批准：返回 passkey 验证参数", s.approveBegin},
		{"POST", "/api/v1/approvals/{id}:approve", accessOperator, "sensitive", "", "批准并执行（请求体带 passkey 断言）", s.approve},
		{"POST", "/api/v1/approvals/{id}:reject", accessOperator, "normal", "", "拒绝", s.reject},
		{"POST", "/api/v1/approvals/{id}:send-code", accessOperator, "normal", "", "把一次性码私聊发到绑定的 Telegram", s.approvalSendCode},

		// 用户侧
		{"POST", "/api/v1/me/session", accessPublic, "normal", "", "终端用户登录", s.meLogin},
		{"DELETE", "/api/v1/me/session", accessPortal, "normal", "", "终端用户退出", s.meLogout},
		{"POST", "/api/v1/me/password:forgot", accessPublic, "normal", "", "发送找回密码邮件", s.meForgot},
		{"POST", "/api/v1/me/password:reset", accessPublic, "normal", "", "凭邮件令牌设置新密码", s.meReset},
		{"PUT", "/api/v1/me/password", accessPortal, "normal", "", "修改密码", s.meChangePassword},
		{"GET", "/api/v1/me/subscription", accessPortal, "read", "", "我的订阅：套餐、用量、到期、订阅链接", s.meSubscription},
		{"POST", "/api/v1/me/subscription:reset-token", accessPortal, "normal", "", "重置订阅链接", s.meResetToken},
		{"GET", "/api/v1/me/traffic", accessPortal, "read", "", "最近 30 天每日用量", s.meTraffic},
		{"GET", "/api/v1/me/rule-profiles", accessPortal, "read", "", "可以自选的分流规则与当前生效的规则", s.meRuleProfiles},
		{"PUT", "/api/v1/me/rule-profile", accessPortal, "normal", "", "选择分流规则（空字符串为跟随套餐默认）", s.meSetRuleProfile},
		{"GET", "/api/v1/me/config", accessPublic, "read", "", "用户侧设置：是否开放注册、是否开通在线支付", s.meConfig},
		{"POST", "/api/v1/me/register", accessPublic, "normal", "", "注册（服务端开放注册时可用）", s.meRegister},
		{"GET", "/api/v1/me/plans", accessPortal, "read", "", "在售套餐、价格、支付方式与余额", s.mePlans},
		{"POST", "/api/v1/me/orders:quote", accessPortal, "read", "", "试算价格（优惠码、余额抵扣）", s.meQuote},
		{"GET", "/api/v1/me/orders", accessPortal, "read", "", "我的订单", s.meOrders},
		{"POST", "/api/v1/me/orders", accessPortal, "normal", "", "下单（续费、购买、换套餐、流量重置包）", s.meOrderCreate},
		{"GET", "/api/v1/me/orders/{id}", accessPortal, "read", "", "订单详情", s.meOrderGet},
		{"POST", "/api/v1/me/orders/{id}:pay", accessPortal, "normal", "", "获取付款链接", s.meOrderPay},
		{"POST", "/api/v1/me/orders/{id}:cancel", accessPortal, "normal", "", "取消待支付订单", s.meOrderCancel},
		{"GET", "/api/v1/me/tickets", accessPortal, "read", "", "我的工单", s.meTickets},
		{"POST", "/api/v1/me/tickets", accessPortal, "normal", "", "提交工单", s.meTicketCreate},
		{"GET", "/api/v1/me/tickets/{id}", accessPortal, "read", "", "工单详情", s.meTicketGet},
		{"POST", "/api/v1/me/tickets/{id}/messages", accessPortal, "normal", "", "回复工单", s.meTicketMessage},
		{"POST", "/api/v1/me/tickets/{id}:close", accessPortal, "normal", "", "关闭工单", s.meTicketClose},
		{"GET", "/api/v1/me/referral", accessPortal, "read", "", "邀请链接、返佣明细与余额", s.meReferral},

		// 支付回调（由支付平台调用，验签后才开通）
		{"GET", "/api/v1/payment/{method}/notify", accessPublic, "normal", "", "支付平台异步回调", s.paymentNotify},
		{"POST", "/api/v1/payment/{method}/notify", accessPublic, "normal", "", "支付平台异步回调", s.paymentNotify},
	}
}
