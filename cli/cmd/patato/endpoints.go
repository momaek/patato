package main

import (
	"encoding/json"
	"time"
)

// CLI 用到的接口。路径与 api/openapi.yaml 一致，endpoints_test.go 检查每一条都在契约里。
const (
	epMe            = "/api/v1/auth/me"
	epLogout        = "/api/v1/auth/logout"
	epGrants        = "/api/v1/auth/cli/grants"
	epGrantToken    = "/api/v1/auth/cli/grants/{id}:token"
	epStatus        = "/api/v1/status"
	epEvents        = "/api/v1/events"
	epAudit         = "/api/v1/audit"
	epUsage         = "/api/v1/stats/usage"
	epTemplates     = "/api/v1/templates"
	epTemplate      = "/api/v1/templates/{id}"
	epNodes         = "/api/v1/nodes"
	epNode          = "/api/v1/nodes/{id}"
	epNodeInstall   = "/api/v1/nodes/{id}/install-cmd"
	epNodeProvision = "/api/v1/nodes:provision"
	epNodeAttach    = "/api/v1/nodes/{id}:attach"
	epNodeDetach    = "/api/v1/nodes/{id}:detach"
	epNodeRotate    = "/api/v1/nodes/{id}:rotate-key"
	epMachines      = "/api/v1/machines"
	epMachine       = "/api/v1/machines/{id}"
	epMachineCmd    = "/api/v1/machines/{id}/install-cmd"
	epMachineRevoke = "/api/v1/machines/{id}:revoke"
	epPlans         = "/api/v1/plans"
	epPlan          = "/api/v1/plans/{id}"
	epUsers         = "/api/v1/users"
	epUser          = "/api/v1/users/{id}"
	epUserTraffic   = "/api/v1/users/{id}:reset-traffic"
	epUserToken     = "/api/v1/users/{id}:reset-token"
	epChange        = "/api/v1/changes/{id}"
	epChangeApply   = "/api/v1/changes/{id}:apply"
	epApprovals     = "/api/v1/approvals"
	epApproval      = "/api/v1/approvals/{id}"

	epAuditExport      = "/api/v1/audit/export"
	epPlanRevisions    = "/api/v1/plans/{id}/revisions"
	epRuleProfiles     = "/api/v1/rule-profiles"
	epRuleProfile      = "/api/v1/rule-profiles/{id}"
	epRulePresets      = "/api/v1/rule-profiles/presets"
	epRuleRefresh      = "/api/v1/rule-profiles/{id}:refresh"
	epOrders           = "/api/v1/orders"
	epOrder            = "/api/v1/orders/{id}"
	epOrderRefund      = "/api/v1/orders/{id}:refund"
	epOrderMarkPaid    = "/api/v1/orders/{id}:mark-paid"
	epOrderCancel      = "/api/v1/orders/{id}:cancel"
	epCoupons          = "/api/v1/coupons"
	epCoupon           = "/api/v1/coupons/{id}"
	epReferrals        = "/api/v1/referrals"
	epReferralSettings = "/api/v1/referrals/settings"
	epReferralRelease  = "/api/v1/referrals/{id}:release"
	epTickets          = "/api/v1/tickets"
	epTicket           = "/api/v1/tickets/{id}"
	epTicketDraft      = "/api/v1/tickets/{id}:draft"
	epTicketClose      = "/api/v1/tickets/{id}:close"
	epReportTraffic    = "/api/v1/reports/traffic"
	epReportRevenue    = "/api/v1/reports/revenue"
	epReportNodes      = "/api/v1/reports/nodes"
	epWebhooks         = "/api/v1/webhooks"
	epWebhook          = "/api/v1/webhooks/{id}"
	epAutomations      = "/api/v1/automations"
	epAutomation       = "/api/v1/automations/{id}"
)

// usedEndpoints 是「方法 路径」，供契约测试检查。
var usedEndpoints = []string{
	"GET " + epMe, "POST " + epLogout, "POST " + epGrants, "POST " + epGrantToken,
	"GET " + epStatus, "GET " + epEvents, "GET " + epAudit, "GET " + epUsage,
	"GET " + epTemplates, "GET " + epTemplate, "POST " + epTemplates, "PATCH " + epTemplate,
	"GET " + epNodes, "GET " + epNode, "GET " + epNodeInstall, "POST " + epNodeProvision, "PATCH " + epNode,
	"POST " + epNodeAttach, "POST " + epNodeDetach, "POST " + epNodeRotate, "DELETE " + epNode,
	"GET " + epMachines, "GET " + epMachine, "GET " + epMachineCmd, "POST " + epMachineRevoke,
	"GET " + epPlans, "GET " + epPlan, "POST " + epPlans, "PATCH " + epPlan, "DELETE " + epPlan,
	"GET " + epRuleProfiles, "GET " + epRuleProfile, "GET " + epRulePresets, "POST " + epRuleProfiles, "PATCH " + epRuleProfile,
	"POST " + epRuleRefresh, "DELETE " + epRuleProfile,
	"GET " + epUsers, "GET " + epUser, "POST " + epUsers, "PATCH " + epUser, "POST " + epUserTraffic,
	"POST " + epUserToken, "DELETE " + epUser,
	"GET " + epChange, "POST " + epChangeApply,
	"GET " + epApprovals, "GET " + epApproval,
	"GET " + epAuditExport, "GET " + epPlanRevisions,
	"GET " + epOrders, "GET " + epOrder, "POST " + epOrderRefund, "POST " + epOrderMarkPaid, "POST " + epOrderCancel,
	"GET " + epCoupons, "GET " + epCoupon, "POST " + epCoupons, "PATCH " + epCoupon,
	"GET " + epReferrals, "GET " + epReferralSettings, "PUT " + epReferralSettings, "POST " + epReferralRelease,
	"GET " + epTickets, "GET " + epTicket, "POST " + epTicketDraft, "POST " + epTicketClose,
	"GET " + epReportTraffic, "GET " + epReportRevenue, "GET " + epReportNodes,
	"GET " + epWebhooks, "POST " + epWebhooks, "DELETE " + epWebhook,
	"GET " + epAutomations, "POST " + epAutomations, "PATCH " + epAutomation, "DELETE " + epAutomation,
}

// 下面是展示需要的字段，对应服务端的视图；完整内容用 --json 查看。

type ref struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type list[T any] struct {
	Items      []T    `json:"items"`
	NextCursor *int64 `json:"next_cursor"`
}

type whoami struct {
	Kind     string `json:"kind"`
	Operator struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"operator"`
	Scope    string `json:"scope"`
	TokenID  int64  `json:"token_id"`
	AdminURL string `json:"admin_url"`
}

type changePlan struct {
	ChangeID string   `json:"change_id"`
	Kind     string   `json:"kind"`
	Summary  string   `json:"summary"`
	Steps    []string `json:"plan"`
	Impact   struct {
		Users int64 `json:"users"`
		Nodes int   `json:"nodes"`
	} `json:"impact"`
	Diff []struct {
		Field  string `json:"field"`
		Before any    `json:"before"`
		After  any    `json:"after"`
	} `json:"diff"`
	DeclaredRisk string          `json:"declared_risk"`
	Risk         string          `json:"risk"`
	RiskReason   string          `json:"risk_reason"`
	Status       string          `json:"status"`
	ExpiresAt    time.Time       `json:"expires_at"`
	ApprovalID   string          `json:"approval_id"`
	Result       json.RawMessage `json:"result"`
	Error        string          `json:"error"`
}

type approvalDetail struct {
	ID          string      `json:"approval_id"`
	Status      string      `json:"status"`
	ExpiresAt   time.Time   `json:"expires_at"`
	CreatedAt   time.Time   `json:"created_at"`
	DecidedBy   string      `json:"decided_by"`
	DecidedVia  string      `json:"decided_via"`
	DecidedAt   *time.Time  `json:"decided_at"`
	Reason      string      `json:"reason"`
	Change      *changePlan `json:"change"`
	Instruction string      `json:"instruction"`
	Required    int         `json:"required"`
	NeedsCode   bool        `json:"needs_code"`
	Votes       []struct {
		Operator string    `json:"operator"`
		Via      string    `json:"via"`
		At       time.Time `json:"at"`
	} `json:"votes"`
	Requester struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
		Claim string `json:"actor_claim"`
		Entry string `json:"entry"`
	} `json:"requester"`
}

type node struct {
	ID              int64           `json:"id"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Region          string          `json:"region"`
	Host            string          `json:"host"`
	Address         string          `json:"address"`
	Protocol        string          `json:"protocol"`
	Backend         string          `json:"backend"`
	Port            int             `json:"port"`
	State           string          `json:"state"`
	InSubscription  bool            `json:"in_subscription"`
	Rate            float64         `json:"rate"`
	Tags            []string        `json:"tags"`
	Plans           []ref           `json:"plans"`
	OnlineUsers     int             `json:"online_users"`
	TodayBytes      int64           `json:"today_bytes"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at"`
	LastPushAt      *time.Time      `json:"last_push_at"`
	Runtime         *nodeRuntime    `json:"runtime"`
	Settings        json.RawMessage `json:"settings"`
	LegacyToken     bool            `json:"allow_legacy_token"`
	Version         int64           `json:"version"`
}

type nodeRuntime struct {
	Running       bool       `json:"running"`
	Error         string     `json:"error"`
	Listening     *bool      `json:"listening"`
	CertExpiresAt *time.Time `json:"cert_expires_at"`
	Users         int        `json:"users"`
	Connections   int        `json:"connections"`
	AppliedAt     *time.Time `json:"applied_at"`
}

type machine struct {
	Host         string     `json:"host"`
	Status       string     `json:"status"`
	Connected    bool       `json:"connected"`
	Hostname     string     `json:"hostname"`
	AgentVersion string     `json:"agent_version"`
	Kernel       string     `json:"kernel"`
	EnrolledAt   *time.Time `json:"enrolled_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	Nodes        []ref      `json:"nodes"`
}

type installCmd struct {
	Backend string `json:"backend"`
	Command string `json:"command"`
	Note    string `json:"note"`
}

type template struct {
	ID       int64          `json:"id"`
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Region   string         `json:"region"`
	Protocol string         `json:"protocol"`
	PortMin  int            `json:"port_min"`
	PortMax  int            `json:"port_max"`
	Rate     float64        `json:"rate"`
	Settings map[string]any `json:"settings"`
	Version  int64          `json:"version"`
}

type plan struct {
	ID             int64            `json:"id"`
	Code           string           `json:"code"`
	Name           string           `json:"name"`
	TransferGB     float64          `json:"transfer_gb"`
	ResetMethod    string           `json:"reset_method"`
	DeviceLimit    int              `json:"device_limit"`
	SpeedLimitMbps int              `json:"speed_limit_mbps"`
	Prices         map[string]int64 `json:"prices"`
	Sellable       bool             `json:"sellable"`
	RuleProfile    *string          `json:"rule_profile"`
	UserCount      int64            `json:"user_count"`
	NodeCount      int64            `json:"node_count"`
	Nodes          []ref            `json:"nodes"`
	Version        int64            `json:"version"`
	UpdatedAt      *time.Time       `json:"updated_at"`
}

type user struct {
	ID             int64      `json:"id"`
	Email          string     `json:"email"`
	Plan           *ref       `json:"plan"`
	State          string     `json:"state"`
	TransferBytes  int64      `json:"transfer_bytes"`
	UsedBytes      int64      `json:"used_bytes"`
	UsedUp         int64      `json:"used_up"`
	UsedDown       int64      `json:"used_down"`
	ExpiresAt      *time.Time `json:"expires_at"`
	NextResetAt    *time.Time `json:"next_reset_at"`
	DeviceLimit    *int       `json:"device_limit"`
	SpeedLimitMbps *int       `json:"speed_limit_mbps"`
	DevicesOnline  int        `json:"devices_online"`
	TokenResetAt   *time.Time `json:"token_reset_at"`
	CreatedAt      *time.Time `json:"created_at"`
	Version        int64      `json:"version"`
}

type auditEntry struct {
	ID         int64      `json:"id"`
	At         *time.Time `json:"at"`
	Subject    string     `json:"subject"`
	ActorClaim string     `json:"actor_claim"`
	Entry      string     `json:"entry"`
	Action     string     `json:"action"`
	Target     string     `json:"target"`
	Risk       string     `json:"risk"`
	ChangeID   string     `json:"change_id"`
	Result     string     `json:"result"`
	Detail     struct {
		Summary string `json:"summary"`
		Error   string `json:"error"`
		Reason  string `json:"reason"`
	} `json:"detail"`
}

var stateNames = map[string]string{"online": "在线", "offline": "离线", "pending": "待接入"}
var backendNames = map[string]string{"native": "patato-node", "uniproxy": "V2bX / XrayR"}
var userStateNames = map[string]string{"active": "使用中", "expired": "已到期", "over_quota": "超额停用", "banned": "已封禁"}
var riskNames = map[string]string{"read": "只读", "normal": "常规", "sensitive": "敏感", "forbidden": "禁止"}
var resetNames = map[string]string{"purchase_day": "按购买日", "calendar_month": "按自然月", "never": "不重置"}
var entryNames = map[string]string{"cli": "CLI", "web": "网页", "telegram": "Telegram", "cli_passkey": "CLI 拉起的 passkey", "portal": "用户侧", "system": "系统",
	"payment": "支付回调", "automation": "自动处置"}
var resultNames = map[string]string{"success": "成功", "pending_approval": "等待审批", "rejected": "已拒绝", "voided": "已作废", "failed": "失败", "vote": "已批准一票"}
var approvalNames = map[string]string{"pending": "待审批", "approved": "已批准", "rejected": "已拒绝", "expired": "已过期", "failed": "执行失败"}
var machineNames = map[string]string{"enrolled": "已接入", "pending_enroll": "等待接入", "not_enrolled": "未接入", "revoked": "已吊销"}

func label(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}
