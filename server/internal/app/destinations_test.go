package app

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/config"
)

// destHarness 建好一台接入的机器、一个原生节点和两名用户，返回机器 token。
func destHarness(t *testing.T, enabled bool) (h *harness, token string, nodeID int64, users [2]int64) {
	h = newHarness(t, func(c *config.Config) { c.Monitoring.Destinations.Enabled = enabled })
	ctx := context.Background()
	expect(t, h.api("POST", "/api/v1/templates", map[string]any{
		"code": "jp-vless", "name": "日本 VLESS", "region": "东京", "protocol": "vless", "port_min": 31000, "port_max": 31010,
		"settings": map[string]any{"tls": 0, "network": "tcp"},
	}), 200)
	expect(t, h.api("POST", "/api/v1/plans", map[string]any{"code": "premium", "name": "高级", "transfer_gb": 100}), 200)
	for i := range 2 {
		email := fmt.Sprintf("d%d@example.com", i)
		expect(t, h.api("POST", "/api/v1/users", map[string]any{"email": email, "plan": "premium", "password": "password-123", "days": 30}), 200)
		u, err := h.a.Store.GetUserByEmail(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		users[i] = u.ID
	}
	r := h.api("GET", "/api/v1/machines/tokyo-01/install-cmd", nil)
	expect(t, r, 200)
	code := regexp.MustCompile(`--code '([^']+)'`).FindStringSubmatch(r.str("command"))
	if code == nil {
		t.Fatalf("install cmd: %s", r.raw)
	}
	r = h.do("POST", nodeapi.PathEnroll, nodeapi.EnrollRequest{Code: code[1], Hostname: "tokyo-01", Version: "test"}, nil)
	expect(t, r, 200)
	token = r.str("token")
	r = h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-vless", "host": "tokyo-01", "address": "127.0.0.1", "plans": []string{"premium"}, "code": "tokyo-vless",
	})
	expect(t, r, 200)
	n, err := h.a.Store.GetNodeByCode(ctx, "tokyo-vless")
	if err != nil {
		t.Fatal(err)
	}
	return h, token, n.ID, users
}

func (h *harness) machine(token, path string, body any) resp {
	return h.do("POST", path, body, map[string]string{"Authorization": "Bearer " + token})
}

// TestDestinationStats 覆盖落地访问统计的服务端：开关随同步下发；上报按小时累加入库、重复批次不重复入账；
// 节点视图按用户求和、可按用户筛选，用户视图可按节点筛选；过期数据由清理任务删除。
func TestDestinationStats(t *testing.T) {
	h, token, nodeID, users := destHarness(t, true)
	ctx := context.Background()
	u1, u2 := users[0], users[1]

	r := h.machine(token, nodeapi.PathSync, nodeapi.SyncRequest{Version: "test"})
	expect(t, r, 200)
	if r.str("settings.destinations") != "true" {
		t.Fatalf("sync settings: %s", r.raw)
	}

	now := time.Now().UTC()
	report := func(seq int64, at time.Time, stats ...nodeapi.DestinationStat) resp {
		return h.machine(token, nodeapi.PathReport, nodeapi.ReportRequest{Spool: "sp1", Seq: seq, At: at,
			Nodes: []nodeapi.NodeReport{{ID: nodeID, Destinations: stats}}})
	}
	expect(t, report(1, now,
		nodeapi.DestinationStat{User: u1, Kind: "domain", Dest: "google.com", Up: 100, Down: 1000, Conns: 2},
		nodeapi.DestinationStat{User: u2, Kind: "domain", Dest: "google.com", Up: 50, Down: 500, Conns: 1},
		nodeapi.DestinationStat{User: u2, Kind: "ip", Dest: "8.8.8.8", Up: 10, Down: 20, Conns: 3},
		nodeapi.DestinationStat{User: u1, Kind: "other", Dest: "ignored", Up: 5, Down: 5, Conns: 1, Merged: 7},
		nodeapi.DestinationStat{User: u1, Kind: "bogus", Dest: "x", Up: 1},
	), 200)
	// 重复的批次只回 ack，不重复入账
	if r := report(1, now, nodeapi.DestinationStat{User: u1, Kind: "domain", Dest: "google.com", Up: 100, Down: 1000, Conns: 2}); r.str("duplicate") != "true" {
		t.Fatalf("duplicate: %s", r.raw)
	}
	// 同一小时的下一批累加
	expect(t, report(2, now, nodeapi.DestinationStat{User: u1, Kind: "domain", Dest: "google.com", Up: 1, Down: 1, Conns: 1}), 200)
	// 已过保留期的小时（例如节点积压后补发的旧批次），清理任务会删除
	old := now.Add(-8 * 24 * time.Hour)
	expect(t, report(3, old, nodeapi.DestinationStat{User: u1, Kind: "domain", Dest: "old.com", Up: 9, Down: 9, Conns: 1}), 200)

	type item struct {
		Kind   string  `json:"kind"`
		Dest   string  `json:"dest"`
		Bytes  float64 `json:"bytes"`
		Conns  float64 `json:"conns"`
		Users  float64 `json:"users"`
		Merged float64 `json:"merged"`
	}
	items := func(r resp) []item {
		t.Helper()
		expect(t, r, 200)
		var out []item
		for _, raw := range r.body["items"].([]any) {
			m := raw.(map[string]any)
			it := item{Kind: m["kind"].(string), Dest: m["dest"].(string), Bytes: m["bytes"].(float64), Conns: m["conns"].(float64)}
			if v, ok := m["users"].(float64); ok {
				it.Users = v
			}
			if v, ok := m["merged"].(float64); ok {
				it.Merged = v
			}
			out = append(out, it)
		}
		return out
	}

	// 节点视图：按用户求和，按流量排序
	got := items(h.api("GET", "/api/v1/nodes/tokyo-vless/destinations?since=24h", nil))
	if len(got) != 3 || got[0].Dest != "google.com" || got[0].Bytes != 100+1000+50+500+2 || got[0].Conns != 4 || got[0].Users != 2 {
		t.Fatalf("node view: %+v", got)
	}
	if got[1].Dest != "8.8.8.8" || got[1].Kind != "ip" || got[2].Kind != "other" || got[2].Dest != nodeapi.DestinationOtherName || got[2].Merged != 7 {
		t.Fatalf("node view tail: %+v", got)
	}
	// 按用户筛选
	got = items(h.api("GET", fmt.Sprintf("/api/v1/nodes/tokyo-vless/destinations?user=%d", u2), nil))
	if len(got) != 2 || got[0].Dest != "google.com" || got[0].Bytes != 550 {
		t.Fatalf("node view for user 2: %+v", got)
	}
	// 时间段覆盖到 8 天前时被限制在保留天数内
	got = items(h.api("GET", "/api/v1/nodes/tokyo-vless/destinations?since=30d", nil))
	for _, it := range got {
		if it.Dest == "old.com" {
			t.Fatalf("since must be capped by retention: %+v", got)
		}
	}
	// 用户视图，可按节点筛选
	got = items(h.api("GET", "/api/v1/users/d0@example.com/destinations", nil))
	if len(got) != 2 || got[0].Dest != "google.com" || got[0].Bytes != 1102 {
		t.Fatalf("user view: %+v", got)
	}
	got = items(h.api("GET", fmt.Sprintf("/api/v1/users/%d/destinations?node=tokyo-vless", u1), nil))
	if len(got) != 2 {
		t.Fatalf("user view filtered by node: %+v", got)
	}
	expect(t, h.api("GET", "/api/v1/users/d0@example.com/destinations?node=nope", nil), 404)
	expect(t, h.api("GET", "/api/v1/nodes/tokyo-vless/destinations?since=abc", nil), 400)
	// 只读接口：普通用户会话拿不到
	expect(t, h.do("GET", "/api/v1/nodes/tokyo-vless/destinations", nil, nil), 401)

	// 用户侧：开启时「我的订阅」带上告知
	cookie := h.login("d0@example.com", "password-123")
	r = h.do("GET", "/api/v1/me/subscription", nil, cookie)
	expect(t, r, 200)
	if r.str("destination_stats.retention_days") != "7" {
		t.Fatalf("portal notice: %s", r.raw)
	}

	// 保留期清理
	var before int
	_ = h.a.Store.Pool.QueryRow(ctx, "SELECT count(*) FROM destination_stats_hourly").Scan(&before)
	if err := h.a.CleanupDestinations(ctx); err != nil {
		t.Fatal(err)
	}
	var after, oldRows int
	_ = h.a.Store.Pool.QueryRow(ctx, "SELECT count(*) FROM destination_stats_hourly").Scan(&after)
	_ = h.a.Store.Pool.QueryRow(ctx, "SELECT count(*) FROM destination_stats_hourly WHERE dest = 'old.com'").Scan(&oldRows)
	if before != 5 || after != 4 || oldRows != 0 {
		t.Fatalf("retention: before=%d after=%d old=%d", before, after, oldRows)
	}
}

// TestDestinationStatsDisabled 默认关闭：同步不下发开关，节点仍在上报（尚未同步到关闭）时服务端也不入账，用户侧不显示告知。
func TestDestinationStatsDisabled(t *testing.T) {
	h, token, nodeID, users := destHarness(t, false)
	r := h.machine(token, nodeapi.PathSync, nodeapi.SyncRequest{Version: "test"})
	expect(t, r, 200)
	if v := r.str("settings.destinations"); v != "<nil>" && v != "false" {
		t.Fatalf("sync settings: %s", r.raw)
	}
	expect(t, h.machine(token, nodeapi.PathReport, nodeapi.ReportRequest{Spool: "sp1", Seq: 1, At: time.Now(),
		Nodes: []nodeapi.NodeReport{{ID: nodeID, Destinations: []nodeapi.DestinationStat{{User: users[0], Kind: "domain", Dest: "a.com", Up: 1, Conns: 1}}}}}), 200)
	var n int
	_ = h.a.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM destination_stats_hourly").Scan(&n)
	if n != 0 {
		t.Fatalf("stored %d rows while disabled", n)
	}
	r = h.api("GET", "/api/v1/nodes/tokyo-vless/destinations", nil)
	expect(t, r, 200)
	if r.str("enabled") != "false" {
		t.Fatalf("enabled flag: %s", r.raw)
	}
	cookie := h.login("d0@example.com", "password-123")
	if r := h.do("GET", "/api/v1/me/subscription", nil, cookie); r.str("destination_stats") != "<nil>" {
		t.Fatalf("portal notice should be hidden: %s", r.raw)
	}
}
