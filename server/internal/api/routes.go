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

		// 用户
		{"GET", "/api/v1/users", accessAdmin, "read", "", "用户列表", s.userList},
		{"GET", "/api/v1/users/{id}", accessAdmin, "read", "", "用户详情", s.userGet},
		{"POST", "/api/v1/users", accessAdmin, "normal", "user.create", "新建用户", s.mutate("user.create", nil)},
		{"PATCH", "/api/v1/users/{id}", accessAdmin, "normal", "user.update", "修改用户（换套餐、续期、限速、封禁）", s.mutate("user.update", pathRef("user"))},
		{"POST", "/api/v1/users/{id}:reset-traffic", accessAdmin, "normal", "user.reset_traffic", "重置用户流量", s.mutate("user.reset_traffic", pathRef("user"))},
		{"POST", "/api/v1/users/{id}:reset-token", accessAdmin, "normal", "user.reset_token", "重置用户订阅", s.mutate("user.reset_token", pathRef("user"))},
		{"DELETE", "/api/v1/users/{id}", accessAdmin, "sensitive", "user.delete", "删除用户", s.mutate("user.delete", pathRef("user"))},

		// 变更计划
		{"GET", "/api/v1/changes/{id}", accessAdmin, "read", "", "变更计划详情", s.changeGet},
		{"POST", "/api/v1/changes/{id}:apply", accessAdmin, "normal", "", "执行预演好的变更计划（敏感计划转为审批）", s.changeApply},

		// 审批
		{"GET", "/api/v1/approvals", accessAdmin, "read", "", "审批列表", s.approvalList},
		{"GET", "/api/v1/approvals/{id}", accessAdmin, "read", "", "审批详情", s.approvalGet},
		{"POST", "/api/v1/approvals/{id}:approve-begin", accessOperator, "sensitive", "", "开始批准：返回 passkey 验证参数", s.approveBegin},
		{"POST", "/api/v1/approvals/{id}:approve", accessOperator, "sensitive", "", "批准并执行（请求体带 passkey 断言）", s.approve},
		{"POST", "/api/v1/approvals/{id}:reject", accessOperator, "normal", "", "拒绝", s.reject},

		// 用户侧
		{"POST", "/api/v1/me/session", accessPublic, "normal", "", "终端用户登录", s.meLogin},
		{"DELETE", "/api/v1/me/session", accessPortal, "normal", "", "终端用户退出", s.meLogout},
		{"POST", "/api/v1/me/password:forgot", accessPublic, "normal", "", "发送找回密码邮件", s.meForgot},
		{"POST", "/api/v1/me/password:reset", accessPublic, "normal", "", "凭邮件令牌设置新密码", s.meReset},
		{"PUT", "/api/v1/me/password", accessPortal, "normal", "", "修改密码", s.meChangePassword},
		{"GET", "/api/v1/me/subscription", accessPortal, "read", "", "我的订阅：套餐、用量、到期、订阅链接", s.meSubscription},
		{"POST", "/api/v1/me/subscription:reset-token", accessPortal, "normal", "", "重置订阅链接", s.meResetToken},
	}
}
