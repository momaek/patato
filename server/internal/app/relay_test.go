package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
)

// relaySetup 建两台中转机器（广州、香港，已接入 patato-node）、一个在线的落地节点和 3 名用户。
type relayEnv struct {
	h            *harness
	node         db.Node
	gz, hk       db.Machine
	gzTok, hkTok string
	subTokens    []string
}

func enrollMachine(t *testing.T, h *harness, host string) (db.Machine, string) {
	t.Helper()
	ctx := context.Background()
	m, err := h.a.Store.EnsureMachine(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	tok := secret.Token("mt")
	code := secret.Token("pe")
	exp := time.Now().Add(time.Hour)
	if err := h.a.Store.SetMachineEnrollment(ctx, db.SetMachineEnrollmentParams{ID: m.ID, EnrollHash: secret.Hash(code), EnrollExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	m, err = h.a.Store.ClaimMachineEnrollment(ctx, db.ClaimMachineEnrollmentParams{TokenHash: secret.Hash(tok), EnrollHash: secret.Hash(code), Hostname: host})
	if err != nil {
		t.Fatal(err)
	}
	return m, tok
}

func newRelayEnv(t *testing.T) *relayEnv {
	h := newHarness(t, nil)
	ctx := context.Background()
	_, _, _, subs := setup(h)
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "la.example.com", "host": "la-01", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	n, err := h.a.Store.GetNodeByCode(ctx, "la-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Store.TouchNodeHeartbeat(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	n, _ = h.a.Store.GetNode(ctx, n.ID)
	env := &relayEnv{h: h, node: n, subTokens: subs}
	env.gz, env.gzTok = enrollMachine(t, h, "gz-01")
	env.hk, env.hkTok = enrollMachine(t, h, "hk-01")
	return env
}

func (e *relayEnv) machineDo(tok, path string, body any) resp {
	return e.h.do("POST", path, body, map[string]string{"Authorization": "Bearer " + tok})
}

func (e *relayEnv) sync(tok, rev string) nodeapi.SyncResponse {
	e.h.t.Helper()
	r := e.machineDo(tok, nodeapi.PathSync, nodeapi.SyncRequest{Version: "test", Nodes: map[int64]nodeapi.KnownNode{}, ForwardsRev: rev})
	expect(e.h.t, r, 200)
	var out nodeapi.SyncResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		e.h.t.Fatal(err)
	}
	return out
}

func (e *relayEnv) sub(token, ua string) string {
	r := e.h.do("GET", "/api/v1/client/subscribe?token="+token, nil, map[string]string{"User-Agent": ua})
	expect(e.h.t, r, 200)
	return string(r.raw)
}

// TestRelayEndToEnd 走一遍中转线路：设置机器网络属性 → 建线路 → 预演与执行给节点加中转入口（端口分配一致）→
// 订阅按入口展开但未生效的入口不下发 → patato-node 同步到转发并上报生效、流量、探测 → 入口出现在订阅里 →
// 中转机器掉线后入口撤下并按机器聚合告警 → 连续探测失败撤下 → 流量汇总进天表 → 停用与删除入口。
func TestRelayEndToEnd(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()

	// NAT 香港：本机 20000-20009 对应外网 30000-30009
	r := h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{
		"public_address": "hk.example.com", "nat": true, "port_ranges": []map[string]any{{"local": "20000-20009", "public_from": 30000}},
	})
	expect(t, r, 200)
	expect(t, h.api("PATCH", "/api/v1/machines/gz-01/network", map[string]any{
		"public_address": "gz.example.com", "port_ranges": []map[string]any{{"local": "20000-20009"}},
	}), 200)
	// NAT 机器必须填端口段
	r = h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{"port_ranges": []any{}, "dry_run": true})
	expect(t, r, 400)

	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "gz-hk", "name": "广州中转", "hops": []string{"gz-01", "hk-01"}}), 200)

	// gz 上 20000 已被一个节点监听：同号端口应为 20001
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET listening_ports = '{20000}' WHERE host = 'gz-01'`); err != nil {
		t.Fatal(err)
	}

	r = h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "gz-hk", "dry_run": true})
	expect(t, r, 200)
	if r.str("risk") != "normal" || r.str("impact.users") != "3" || !strings.Contains(r.raw2(), "gz-01:20001") || !strings.Contains(r.raw2(), "hk-01:20001（外网 30001）") {
		t.Fatalf("dry run: %s", r.raw)
	}
	changeID := r.str("change_id")
	r = h.api("POST", "/api/v1/changes/"+changeID+":apply", nil)
	expect(t, r, 200)
	if r.str("status") != "applied" {
		t.Fatalf("apply: %s", r.raw)
	}
	entries, _ := h.a.Store.ListNodeEntries(ctx, env.node.ID)
	if len(entries) != 2 {
		t.Fatalf("want implicit direct entry registered + relay entry, got %+v", entries)
	}
	relayEntry := entries[1]
	fs, _ := h.a.Store.ListEntryForwards(ctx, relayEntry.ID)
	if len(fs) != 2 || fs[0].ListenPort != 20001 || fs[0].TargetHost != "hk.example.com" || fs[0].TargetPort != 30001 ||
		fs[1].ListenPort != 20001 || fs[1].PublicPort != 30001 || fs[1].TargetHost != "la.example.com" || fs[1].TargetPort != int32(env.node.Port) {
		t.Fatalf("forwards (dry run and apply must agree): %+v", fs)
	}

	// 转发还没生效：订阅里只有直连入口
	sub := env.sub(env.subTokens[0], "clash-verge/v1.7")
	if strings.Contains(sub, "广州中转") || !strings.Contains(sub, "la.example.com") {
		t.Fatalf("pending relay entry must not be in subscription:\n%s", sub)
	}

	// 下一跳域名由服务端解析后下发
	h.a.Native.SetResolver(fakeIPResolver{"hk.example.com": "198.51.100.2", "la.example.com": "203.0.113.9"})
	s := env.sync(env.gzTok, "")
	if s.Forwards == nil || len(s.Forwards.Items) != 1 || s.Forwards.Items[0].TargetIP != "198.51.100.2" || s.Forwards.Items[0].TargetPort != 30001 ||
		s.Forwards.Items[0].ListenPort != 20001 || s.Settings.ProbeInterval == 0 {
		t.Fatalf("gz sync: %+v %+v", s.Forwards, s.Settings)
	}
	gzRev := s.Forwards.Rev
	if again := env.sync(env.gzTok, gzRev); again.Forwards != nil {
		t.Fatal("unchanged forwards must not be resent")
	}
	hs := env.sync(env.hkTok, "")
	if hs.Forwards == nil || len(hs.Forwards.Items) != 1 || hs.Forwards.Items[0].TargetIP != "203.0.113.9" {
		t.Fatalf("hk sync: %+v", hs.Forwards)
	}

	// 上报：两跳都生效，带流量与探测
	now := time.Now()
	report := func(tok, spool string, seq int64, fr nodeapi.ForwardReport, listening []int) resp {
		return env.machineDo(tok, nodeapi.PathReport, nodeapi.ReportRequest{Spool: spool, Seq: seq, At: now,
			Forwards: []nodeapi.ForwardReport{fr}, Relay: &nodeapi.RelayStatus{Mode: "nft"}, Listening: listening})
	}
	ok1 := nodeapi.ForwardReport{ID: fs[0].ID, Up: 1000, Down: 9000, Applied: true, Mode: "nft", At: now,
		Probe: &nodeapi.ProbeResult{OK: 1, AvgMS: 12, Last: true, At: now}}
	expect(t, report(env.gzTok, "s1", 1, ok1, []int{22, 20000, 20001}), 200)
	// 重复上报不重复入账
	r = report(env.gzTok, "s1", 1, ok1, nil)
	if r.str("duplicate") != "true" {
		t.Fatalf("duplicate: %s", r.raw)
	}
	expect(t, report(env.hkTok, "s2", 1, nodeapi.ForwardReport{ID: fs[1].ID, Up: 1100, Down: 9100, Applied: true, Mode: "nft", At: now}, nil), 200)

	sub = env.sub(env.subTokens[0], "clash-verge/v1.7")
	if !strings.Contains(sub, "la-01") && !strings.Contains(sub, env.node.Name+" · 广州中转") {
		t.Fatalf("active relay entry must be in subscription:\n%s", sub)
	}
	if !strings.Contains(sub, "gz.example.com") || !strings.Contains(sub, "20001") {
		t.Fatalf("subscription must use first hop address and public port:\n%s", sub)
	}
	// 各格式都按入口展开（Surge、Loon 不下发 VLESS，与 Xboard 一致，不在这里检查）
	for _, ua := range []string{"v2rayN", "sing-box", "quantumult"} {
		out := env.sub(env.subTokens[0], ua)
		if b, err := base64.StdEncoding.DecodeString(out); err == nil {
			out = string(b)
		}
		if !strings.Contains(out, "gz.example.com") || !strings.Contains(out, "20001") {
			t.Errorf("%s subscription missing relay entry:\n%s", ua, out)
		}
	}

	// 线路视图与流量
	r = h.api("GET", "/api/v1/routes/gz-hk", nil)
	expect(t, r, 200)
	if r.str("today_up") != "1000" || r.str("today_down") != "9000" {
		t.Fatalf("route traffic uses first hop: %s", r.raw)
	}
	r = h.api("GET", "/api/v1/nodes/la-01/entries", nil)
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), `"available":true`) || !strings.Contains(r.raw2(), `"probe_success_rate_24h":1`) {
		t.Fatalf("entries view: %s", r.raw)
	}
	gz, _ := h.a.Store.GetMachine(ctx, env.gz.ID)
	if len(gz.ListeningPorts) != 3 {
		t.Fatalf("listening ports: %v", gz.ListeningPorts)
	}
	if err := h.a.Relay.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	r = h.api("GET", "/api/v1/routes/gz-hk/traffic?days=2", nil)
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), `"up":1000`) {
		t.Fatalf("daily rollup: %s", r.raw)
	}

	// 下一台机器（落地）IP 变化：解析任务更新 target_ip，下次同步重新下发
	h.a.Native.SetResolver(fakeIPResolver{"hk.example.com": "198.51.100.2", "la.example.com": "203.0.113.10"})
	if n, err := h.a.Native.RefreshTargets(ctx); err != nil || n != 1 {
		t.Fatalf("refresh targets: %d %v", n, err)
	}
	if hs2 := env.sync(env.hkTok, hs.Forwards.Rev); hs2.Forwards == nil || hs2.Forwards.Items[0].TargetIP != "203.0.113.10" {
		t.Fatalf("re-push on IP change: %+v", hs2.Forwards)
	}

	// 连续 3 次探测失败：入口撤下并告警；恢复后重新下发并解决事件
	fail3 := nodeapi.ForwardReport{ID: fs[0].ID, Applied: true, Mode: "nft", At: now,
		Probe: &nodeapi.ProbeResult{Fail: 3, Consecutive: 3, At: now}}
	expect(t, report(env.gzTok, "s1", 2, fail3, nil), 200)
	if strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "gz.example.com") {
		t.Fatal("entry must be withdrawn after 3 probe failures")
	}
	if !hasOpenEvent(t, h, events.RouteProbeFailed) {
		t.Fatal("route_probe_failed event")
	}
	expect(t, report(env.gzTok, "s1", 3, ok1, nil), 200)
	if !strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "gz.example.com") || hasOpenEvent(t, h, events.RouteProbeFailed) {
		t.Fatal("entry must come back after probe recovers")
	}

	// 应用失败：告警
	expect(t, report(env.gzTok, "s1", 4, nodeapi.ForwardReport{ID: fs[0].ID, Applied: false, Mode: "nft", Error: "net.ipv4.ip_forward 没有开启"}, nil), 200)
	if !hasOpenEvent(t, h, events.ForwardApplyFailed) {
		t.Fatal("forward_apply_failed event")
	}
	expect(t, report(env.gzTok, "s1", 5, ok1, nil), 200)

	// 香港掉线：一个心跳周期内入口撤下，按机器聚合发一条告警
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET last_seen_at = now() - interval '1 hour' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET last_seen_at = now() WHERE host = 'gz-01'`); err != nil {
		t.Fatal(err)
	}
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "gz.example.com") {
		t.Fatal("entry through offline hop must be withdrawn")
	}
	evs, _ := h.a.Store.ListEventsAfter(ctx, db.ListEventsAfterParams{ID: 0, Limit: 500})
	hopEvents := 0
	for _, e := range evs {
		if e.Kind == events.RouteHopOffline {
			hopEvents++
			if !strings.Contains(e.Message, "hk-01") || !strings.Contains(e.Message, "1 个入口") || !strings.Contains(e.Message, "3 名用户") {
				t.Fatalf("hop offline message: %s", e.Message)
			}
		}
	}
	if hopEvents != 1 {
		t.Fatalf("want exactly one aggregated route_hop_offline event, got %d", hopEvents)
	}
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}

	// 所有入口都不可用时不清空订阅：节点离线也照样下发（沿用现有回退）
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE nodes SET state = 'offline'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "la.example.com") {
		t.Fatal("subscription must not be emptied when no entry is available")
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE nodes SET state = 'online'`); err != nil {
		t.Fatal(err)
	}

	// 停用入口是敏感操作；删除入口释放端口，下次同步列表为空
	r = h.api("POST", fmt.Sprintf("/api/v1/nodes/la-01/entries/%d:disable", relayEntry.ID), map[string]any{"dry_run": true})
	expect(t, r, 200)
	if r.str("risk") != "sensitive" {
		t.Fatalf("disable must be sensitive: %s", r.raw)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `DELETE FROM forwards WHERE entry_id = $1`, relayEntry.ID); err != nil {
		t.Fatal(err)
	}
	if empty := env.sync(env.gzTok, gzRev); empty.Forwards == nil || len(empty.Forwards.Items) != 0 {
		t.Fatalf("deleted forwards must sync as an empty list: %+v", empty.Forwards)
	}
}

func (r resp) raw2() string { return string(r.raw) }

func hasOpenEvent(t *testing.T, h *harness, kind string) bool {
	t.Helper()
	var n int
	if err := h.a.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE kind = $1 AND resolved_at IS NULL`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

type fakeIPResolver map[string]string

func (f fakeIPResolver) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	ip := net.ParseIP(f[host])
	if ip == nil {
		return nil, fmt.Errorf("no such host %s", host)
	}
	return []net.IP{ip}, nil
}

// TestRelayPortExhausted 端口段用尽时预演返回 port_exhausted，并指出机器和端口段；route.apply 一次给多个节点分配不重复的端口。
func TestRelayPortExhaustedAndApply(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	expect(t, h.api("PATCH", "/api/v1/machines/gz-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "21000-21001"}}}), 200)
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "gz", "name": "广州", "hops": []string{"gz-01"}}), 200)
	// 第二个节点
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "sg.example.com", "host": "sg-01", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	r := h.api("POST", "/api/v1/routes/gz:apply", map[string]any{"plan": "premium", "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "gz-01:21000") || !strings.Contains(r.raw2(), "gz-01:21001") {
		t.Fatalf("route.apply must allocate distinct ports: %s", r.raw)
	}
	expect(t, h.api("POST", "/api/v1/changes/"+r.str("change_id")+":apply", nil), 200)
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "us.example.com", "host": "us-01", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	r = h.api("POST", "/api/v1/nodes/us-01/entries", map[string]any{"route": "gz", "dry_run": true})
	expect(t, r, 409)
	if r.str("error.code") != "port_exhausted" || !strings.Contains(r.str("error.message"), "gz-01") || !strings.Contains(r.str("error.message"), "21000-21001") {
		t.Fatalf("port_exhausted: %s", r.raw)
	}
	// 线路上还有入口时不能改跳、不能删除
	expect(t, h.api("PATCH", "/api/v1/routes/gz", map[string]any{"hops": []string{"hk-01"}, "dry_run": true}), 409)
	expect(t, h.api("DELETE", "/api/v1/routes/gz", map[string]any{"dry_run": true}), 409)
}

// TestRelayManualAndAdopt 手写转发登记：不下发给 patato-node；自定义入口经链路发现确认后登记线路，订阅内容不变。
func TestRelayManualAndAdopt(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "gz-hk", "name": "广州中转", "hops": []string{"gz-01", "hk-01"}}), 200)
	r := h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"name": "洛杉矶 广州中转", "address": "gz.example.com", "port": 60001})
	expect(t, r, 200)
	entryID := int64(r.body["result"].(map[string]any)["entry_id"].(float64))
	before := env.sub(env.subTokens[0], "clash-verge/v1.7")
	if !strings.Contains(before, "洛杉矶 广州中转") || !strings.Contains(before, "60001") {
		t.Fatalf("custom entry in subscription:\n%s", before)
	}

	// 链路发现：不带快照时返回采集命令
	r = h.api("POST", "/api/v1/routes:discover", map[string]any{"node": "la-01", "entry": entryID})
	expect(t, r, 200)
	if !strings.Contains(r.str("discover_command"), "iptables-save") {
		t.Fatalf("discover command: %s", r.raw)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET tolato_ip = '198.51.100.1' WHERE host = 'gz-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET tolato_ip = '198.51.100.2' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}
	r = h.api("POST", "/api/v1/routes:discover", map[string]any{"node": "la-01", "entry": entryID, "host": "198.51.100.1", "snapshots": map[string]string{
		"gz-01": "### iptables\n-A PREROUTING -p tcp -m tcp --dport 60000:62000 -j DNAT --to-destination 198.51.100.2\n",
		"hk-01": fmt.Sprintf("### socat\nsocat TCP4-LISTEN:60001,fork TCP4:la.example.com:%d\n", env.node.Port),
	}})
	expect(t, r, 200)
	if r.str("trace.complete") != "true" || !strings.Contains(r.raw2(), `"hops":["gz-01","hk-01"]`) {
		t.Fatalf("discover: %s", r.raw)
	}

	r = h.api("POST", fmt.Sprintf("/api/v1/nodes/la-01/entries/%d:adopt", entryID), map[string]any{"route": "gz-hk", "forwards": []map[string]any{
		{"machine": "gz-01", "listen_port": 60001, "target_host": "198.51.100.2", "target_port": 60001},
		{"machine": "hk-01", "listen_port": 60001, "target_host": "la.example.com", "target_port": env.node.Port},
	}})
	expect(t, r, 200)
	if r.str("status") != "applied" {
		t.Fatalf("adopt: %s", r.raw)
	}
	if after := env.sub(env.subTokens[0], "clash-verge/v1.7"); after != before {
		t.Fatalf("adopting a route must not change the subscription:\n%s\n---\n%s", before, after)
	}
	// 手写转发不下发
	if s := env.sync(env.gzTok, ""); s.Forwards == nil || len(s.Forwards.Items) != 0 {
		t.Fatalf("manual forwards must not be synced: %+v", s.Forwards)
	}
	// 手写转发占用的端口不会被再分配
	expect(t, h.api("PATCH", "/api/v1/machines/gz-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "60001-60002"}}}), 200)
	expect(t, h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "60001-60002"}}}), 200)
	r = h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "gz-hk", "suffix": "托管", "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "gz-01:60002") || !strings.Contains(r.raw2(), "hk-01:60002") {
		t.Fatalf("must avoid ports registered by manual forwards: %s", r.raw)
	}
	// 中转机器掉线：手写入口同样撤下
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET last_seen_at = now() - interval '1 hour' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "洛杉矶 广州中转") {
		t.Fatal("manual entry through an offline hop must be withdrawn")
	}
}

// TestRelayEntryAddressOverride 经线路的入口可以覆盖订阅里的第一跳地址和端口（第一跳前面还有一层运营者自管的转发，
// 例如入口机整段 DNAT 到中转）：转发仍按分配的端口生成，预演、入口列表与订阅都用覆盖后的地址和端口。
func TestRelayEntryAddressOverride(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()
	expect(t, h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "21001-21001"}}}), 200)
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "hk", "name": "香港中转", "hops": []string{"hk-01"}}), 200)

	// 端口不合法时拒绝
	expect(t, h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "hk", "port": 70000, "dry_run": true}), 400)

	r := h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{
		"route": "hk", "name": "洛杉矶 香港中转", "address": "entry.example.com", "port": 61001, "dry_run": true,
	})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "hk-01:21001") || !strings.Contains(r.raw2(), "entry.example.com:61001") {
		t.Fatalf("plan must allocate the hop port and show the overridden entry address: %s", r.raw)
	}
	r = h.api("POST", "/api/v1/changes/"+r.str("change_id")+":apply", nil)
	expect(t, r, 200)
	entryID := int64(r.body["result"].(map[string]any)["entry_id"].(float64))
	e, err := h.a.Store.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if e.RouteID == nil || e.Address != "entry.example.com" || e.Port != 61001 {
		t.Fatalf("entry must keep the overridden address and port: %+v", e)
	}
	fs, _ := h.a.Store.ListEntryForwards(ctx, entryID)
	if len(fs) != 1 || fs[0].ListenPort != 21001 || fs[0].Mode != "nft" {
		t.Fatalf("forward must still use the allocated port: %+v", fs)
	}

	// 转发生效后，订阅用覆盖后的地址和端口，而不是中转机器
	h.a.Native.SetResolver(fakeIPResolver{"la.example.com": "203.0.113.9"})
	s := env.sync(env.hkTok, "")
	if s.Forwards == nil || len(s.Forwards.Items) != 1 {
		t.Fatalf("hk sync: %+v", s.Forwards)
	}
	now := time.Now()
	expect(t, env.machineDo(env.hkTok, nodeapi.PathReport, nodeapi.ReportRequest{Spool: "s1", Seq: 1, At: now,
		Forwards: []nodeapi.ForwardReport{{ID: fs[0].ID, Applied: true, Mode: "nft", At: now}}}), 200)
	sub := env.sub(env.subTokens[0], "clash-verge/v1.7")
	if !strings.Contains(sub, "洛杉矶 香港中转") || !strings.Contains(sub, "entry.example.com") || !strings.Contains(sub, "61001") {
		t.Fatalf("subscription must use the overridden entry address and port:\n%s", sub)
	}
	if strings.Contains(sub, "hk-01") || strings.Contains(sub, "21001") {
		t.Fatalf("subscription must not expose the hop machine or its port:\n%s", sub)
	}
	r = h.api("GET", "/api/v1/nodes/la-01/entries", nil)
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), `"host":"entry.example.com","port":61001`) {
		t.Fatalf("entries view must show the overridden endpoint: %s", r.raw)
	}
}

// TestNodeUpdatePortsSeparately 修改对外端口不覆盖监听端口；中转入口最后一跳跟着对外端口走。
func TestNodeUpdatePortsSeparately(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "gz", "name": "广州", "hops": []string{"gz-01"}}), 200)
	expect(t, h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "gz"}), 200)
	expect(t, h.api("PATCH", "/api/v1/nodes/la-01", map[string]any{"port": 8443}), 200)
	n, _ := h.a.Store.GetNode(ctx, env.node.ID)
	if n.Port != 8443 || n.ServerPort != env.node.ServerPort {
		t.Fatalf("port must not overwrite server_port: %+v", n)
	}
	entries, _ := h.a.Store.ListNodeEntries(ctx, n.ID)
	fs, _ := h.a.Store.ListEntryForwards(ctx, entries[len(entries)-1].ID)
	if fs[0].TargetPort != 8443 {
		t.Fatalf("last hop must follow node public port: %+v", fs[0])
	}
	expect(t, h.api("PATCH", "/api/v1/nodes/la-01", map[string]any{"server_port": 9443}), 200)
	n, _ = h.a.Store.GetNode(ctx, env.node.ID)
	if n.Port != 8443 || n.ServerPort != 9443 {
		t.Fatalf("server_port: %+v", n)
	}
}

// TestNodeUpdateHost 从 Xboard 导入的节点没有机器名：先填上 host 才能改由 patato-node 承载，填的时候检查监听端口冲突。
func TestNodeUpdateHost(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	setup(h)
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "203.0.113.20", "code": "imported", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "203.0.113.21", "host": "tokyo-01", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	n, err := h.a.Store.GetNodeByCode(ctx, "imported")
	if err != nil {
		t.Fatal(err)
	}
	if n.Host != "" {
		t.Fatalf("node provisioned by address must have no host: %+v", n)
	}

	r := h.api("PATCH", "/api/v1/nodes/imported", map[string]any{"backend": "native", "dry_run": true})
	expect(t, r, 400)
	if r.str("error.code") != "missing_host" {
		t.Fatalf("want missing_host: %s", r.raw)
	}
	// tokyo-01 上同一个监听端口已有节点
	r = h.api("PATCH", "/api/v1/nodes/imported", map[string]any{"host": "tokyo-01", "dry_run": true})
	expect(t, r, 409)
	if r.str("error.code") != "port_taken" {
		t.Fatalf("want port_taken: %s", r.raw)
	}

	expect(t, h.api("PATCH", "/api/v1/nodes/imported", map[string]any{"host": " tokyo-02 ", "backend": "native"}), 200)
	n, _ = h.a.Store.GetNode(ctx, n.ID)
	if n.Host != "tokyo-02" || n.Backend != "native" {
		t.Fatalf("host and backend: %+v", n)
	}
	r = h.api("PATCH", "/api/v1/nodes/imported", map[string]any{"host": "", "dry_run": true})
	expect(t, r, 400)
	if r.str("error.code") != "missing_host" {
		t.Fatalf("native node must keep host: %s", r.raw)
	}
}
