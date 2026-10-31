package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/store/db"
)

// PanelHostKey 是面板主机执行检查结果在 settings 表里的键，由后台任务写入。
const PanelHostKey = "tolato.panel_host"

// PanelHostStatus 记录最近一次检查：tolato 中有没有针对面板主机的执行记录。
type PanelHostStatus struct {
	Status    string    `json:"status"` // ok / alert / unsupported / error / not_configured
	CheckedAt time.Time `json:"checked_at"`
	Message   string    `json:"message"`
}

type todo struct {
	Kind      string     `json:"kind"`
	Severity  string     `json:"severity"`
	Title     string     `json:"title"`
	Detail    string     `json:"detail"`
	Ref       string     `json:"ref"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	At        time.Time  `json:"at"`
}

// status 回答「现在有什么不对劲」：待审批、未解决的告警、节点和用户的整体情况。
func (s *Server) status(c *gin.Context) {
	now := time.Now()
	todos := []todo{}
	pending := "pending"
	aprs, err := s.d.Store.ListApprovals(c, db.ListApprovalsParams{Status: &pending, Limit: 50})
	if err != nil {
		fail(c, err)
		return
	}
	for _, a := range aprs {
		if now.After(a.ExpiresAt) {
			continue
		}
		var imp struct {
			Users int64 `json:"users"`
		}
		_ = json.Unmarshal(a.Impact, &imp)
		exp := a.ExpiresAt
		detail := fmt.Sprintf("影响 %d 名用户。由 %s 发起", imp.Users, a.SubjectLabel)
		if a.RiskReason != "" {
			detail = a.RiskReason + "。由 " + a.SubjectLabel + " 发起"
		}
		todos = append(todos, todo{Kind: "approval", Severity: "warning", Title: a.Summary, Detail: detail,
			Ref: a.ID, ExpiresAt: &exp, At: a.CreatedAt})
	}
	events, err := s.d.Store.ListOpenEvents(c, 50)
	if err != nil {
		fail(c, err)
		return
	}
	for _, e := range events {
		if e.Severity == "info" {
			continue
		}
		todos = append(todos, todo{Kind: e.Kind, Severity: e.Severity, Title: e.Message, Ref: e.Subject, At: e.CreatedAt})
	}

	nodes, err := s.d.Store.ListNodes(c)
	if err != nil {
		fail(c, err)
		return
	}
	nodeCounts := map[string]int{"online": 0, "offline": 0, "pending": 0}
	for _, n := range nodes {
		nodeCounts[n.State]++
	}
	views, _ := s.nodeViews(c, nodes, false)

	users, err := s.d.Store.UserStateCounts(c)
	if err != nil {
		fail(c, err)
		return
	}
	count := func(d time.Duration) int64 {
		n, _ := s.d.Store.CountExpiringBetween(c, db.CountExpiringBetweenParams{FromAt: now, ToAt: now.Add(d)})
		return n
	}
	endOfDay := time.Date(now.In(s.loc).Year(), now.In(s.loc).Month(), now.In(s.loc).Day()+1, 0, 0, 0, 0, s.loc)
	todayBytes, _ := s.d.Store.TotalTrafficOnDay(c, s.today())
	online, _ := s.d.Store.CountOnlineUsers(c, now.Add(-s.d.Cfg.Node.DeviceTTL.D()))

	panel := PanelHostStatus{Status: "not_configured", Message: "未配置 tolato.panel_host"}
	if raw, err := s.d.Store.GetSetting(c, PanelHostKey); err == nil {
		_ = json.Unmarshal(raw, &panel)
	}

	ok(c, gin.H{
		"todo":  todos,
		"nodes": gin.H{"total": len(nodes), "online": nodeCounts["online"], "offline": nodeCounts["offline"], "pending": nodeCounts["pending"], "items": views},
		"users": gin.H{"total": users.Total, "active": users.Active, "expired": users.Expired, "over_quota": users.OverQuota, "banned": users.Banned},
		"expiring": gin.H{
			"today":  count(endOfDay.Sub(now)),
			"days_3": count(72 * time.Hour),
			"days_7": count(7 * 24 * time.Hour),
		},
		"usage":      gin.H{"today_bytes": todayBytes, "online_users": online},
		"panel_host": panel,
		"updated_at": now,
	})
}

// InstallCmd 是在节点机器上安装节点后端的说明。
type InstallCmd struct {
	Backend string `json:"backend"`
	Command string `json:"command"`
	Config  any    `json:"config"`
	Note    string `json:"note"`
}

// V2bXInstall 生成 V2bX 的安装命令：安装（已安装则跳过）、写入只含这个节点的配置、重启。
func V2bXInstall(apiHost string, n db.Node, key string) InstallCmd {
	nodeType := n.Protocol
	core := "xray"
	switch n.Protocol {
	case "hysteria":
		nodeType, core = "hysteria2", "sing"
		if proto.Parse(n.Settings).Int("version") == 1 {
			nodeType = "hysteria"
		}
	case "tuic", "anytls":
		core = "sing"
	}
	cfg := map[string]any{
		"Log":   map[string]any{"Level": "error", "Output": ""},
		"Cores": []map[string]any{{"Type": core, "Log": map[string]any{"Level": "error"}}},
		"Nodes": []map[string]any{{
			"Core": core, "ApiHost": apiHost, "ApiKey": key, "NodeID": n.ID, "NodeType": nodeType,
			"Timeout": 30, "ListenIP": "0.0.0.0", "SendIP": "0.0.0.0", "DeviceOnlineMinTraffic": 200,
			"EnableProxyProtocol": false, "CertConfig": map[string]any{"CertMode": "none"},
		}},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	cmd := strings.Join([]string{
		"set -e",
		"command -v V2bX >/dev/null 2>&1 || (yes n | bash <(curl -Ls https://raw.githubusercontent.com/wyx2685/V2bX-script/master/install.sh))",
		"mkdir -p /etc/V2bX",
		"cat > /etc/V2bX/config.json <<'PATATO_EOF'",
		string(b),
		"PATATO_EOF",
		"systemctl restart V2bX",
	}, "\n")
	return InstallCmd{
		Backend: "v2bx",
		Command: cmd,
		Config:  cfg,
		Note:    fmt.Sprintf("在 %s 上原样执行。节点 %s 收到首次心跳后自动转为在线并下发到订阅。", firstNonEmpty(n.Host, n.Address), n.Code),
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
