package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/approval"
)

// approveChange 执行一个敏感计划并以运营者身份批准。
func approveChange(t *testing.T, h *harness, changeID string) {
	t.Helper()
	r := h.api("POST", "/api/v1/changes/"+changeID+":apply", nil)
	expect(t, r, 202)
	d, err := h.a.Approvals.Approve(context.Background(), r.str("approval_id"), h.op, approval.ViaWeb, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Change.Status != "applied" {
		t.Fatalf("change not applied: %+v", d.Change)
	}
}

// TestRouteRehopWithEntries 线路上已有入口时就地增删跳：保留的跳转发不动（ID、端口不变），
// 新加的第一跳沿用订阅端口并转到原第一跳，入口上覆盖的地址随第一跳变化清空；删掉跳后上一跳改为直连下一跳。
func TestRouteRehopWithEntries(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()

	expect(t, h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{
		"public_address": "203.0.113.20", "port_ranges": []map[string]any{{"local": "61001-61014"}},
	}), 200)
	expect(t, h.api("PATCH", "/api/v1/machines/gz-01/network", map[string]any{
		"public_address": "gz.example.com", "port_ranges": []map[string]any{{"local": "61001-61014"}, {"local": "40000-40009"}},
	}), 200)
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "hk", "name": "香港中转", "hops": []string{"hk-01"}}), 200)
	// 现网的样子：入口机整段 DNAT 到香港，入口覆盖了订阅地址；香港上占住 61001，让入口分到 61002
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET listening_ports = '{61001}' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}
	r := h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "hk", "address": "gz.example.com", "port": 61002})
	expect(t, r, 200)
	entries, _ := h.a.Store.ListNodeEntries(ctx, env.node.ID)
	entry := entries[len(entries)-1]
	before, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	if len(before) != 1 || before[0].ListenPort != 61002 {
		t.Fatalf("setup forwards: %+v", before)
	}
	hkFwd := before[0]

	// 没接入 patato-node 的机器不能加进来
	expect(t, h.api("PATCH", "/api/v1/machines/cn-99/network", map[string]any{"public_address": "198.51.100.99"}), 200)
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"cn-99", "hk-01"}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "machine_not_enrolled" {
		t.Fatalf("unenrolled hop: %s", r.raw)
	}

	// 在前面加一跳 gz-01：同号端口，转到香港原来的端口，订阅不变
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"gz-01", "hk-01"}, "dry_run": true})
	expect(t, r, 200)
	if r.str("risk") != "sensitive" || !strings.Contains(r.raw2(), "gz-01:61002 → 203.0.113.20:61002（新增）") ||
		!strings.Contains(r.raw2(), "hk-01:61002") || !strings.Contains(r.raw2(), "订阅内容不变") {
		t.Fatalf("prepend dry run: %s", r.raw)
	}
	approveChange(t, h, r.str("change_id"))
	after, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	if len(after) != 2 || after[0].ListenPort != 61002 || after[0].TargetHost != "203.0.113.20" || after[0].TargetPort != 61002 ||
		after[1].ID != hkFwd.ID || after[1].ListenPort != 61002 || after[1].TargetHost != hkFwd.TargetHost {
		t.Fatalf("prepend forwards: %+v", after)
	}
	e2, _ := h.a.Store.GetEntry(ctx, entry.ID)
	if e2.Address != "" || e2.Port != 0 {
		t.Fatalf("override must be cleared when first hop changes: %+v", e2)
	}
	h.a.Native.SetResolver(fakeIPResolver{"la.example.com": "203.0.113.9"})
	if s := env.sync(env.gzTok, ""); s.Forwards == nil || len(s.Forwards.Items) != 1 || s.Forwards.Items[0].TargetIP != "203.0.113.20" ||
		s.Forwards.Items[0].ListenPort != 61002 {
		t.Fatalf("gz sync: %+v", s.Forwards)
	}
	// 两跳都生效后，订阅仍是 gz.example.com:61002
	now := time.Now()
	for _, x := range []struct {
		tok, spool string
		id         int64
	}{{env.gzTok, "g", after[0].ID}, {env.hkTok, "h", after[1].ID}} {
		expect(t, env.machineDo(x.tok, nodeapi.PathReport, nodeapi.ReportRequest{Spool: x.spool, Seq: 1, At: now,
			Forwards: []nodeapi.ForwardReport{{ID: x.id, Applied: true, Mode: "nft", At: now}}, Relay: &nodeapi.RelayStatus{Mode: "nft"}}), 200)
	}
	if sub := env.sub(env.subTokens[0], "clash-verge/v1.7"); !strings.Contains(sub, "gz.example.com") || !strings.Contains(sub, "61002") {
		t.Fatalf("subscription after prepend:\n%s", sub)
	}

	// 删掉第一跳：香港的转发仍是原来那条，订阅改为香港的地址
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"hk-01"}, "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "删除 gz-01:61002 的转发") || !strings.Contains(r.raw2(), "gz.example.com:61002 → 203.0.113.20:61002") {
		t.Fatalf("remove dry run: %s", r.raw)
	}
	approveChange(t, h, r.str("change_id"))
	last, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	if len(last) != 1 || last[0].ID != hkFwd.ID || last[0].Hop != 0 {
		t.Fatalf("remove forwards: %+v", last)
	}
	if s := env.sync(env.gzTok, ""); s.Forwards == nil || len(s.Forwards.Items) != 0 {
		t.Fatalf("gz must drop its forward: %+v", s.Forwards)
	}

	// 三跳再删中间跳：上一跳改为直接转到下一跳
	enrollMachine(t, h, "mid-01")
	expect(t, h.api("PATCH", "/api/v1/machines/mid-01/network", map[string]any{"public_address": "198.51.100.40"}), 200)
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"gz-01", "mid-01", "hk-01"}, "dry_run": true})
	expect(t, r, 200)
	approveChange(t, h, r.str("change_id"))
	three, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	if len(three) != 3 || three[0].TargetHost != "198.51.100.40" || three[1].TargetHost != "203.0.113.20" || three[2].ID != hkFwd.ID {
		t.Fatalf("three hops: %+v", three)
	}
	// 保留的机器调换顺序被拒绝
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"hk-01", "gz-01"}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "route_hops_reordered" {
		t.Fatalf("reorder: %s", r.raw)
	}
	r = h.api("PATCH", "/api/v1/routes/hk", map[string]any{"hops": []string{"gz-01", "hk-01"}, "dry_run": true})
	expect(t, r, 200)
	approveChange(t, h, r.str("change_id"))
	two, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	if len(two) != 2 || two[0].ID != three[0].ID || two[0].TargetHost != "203.0.113.20" || two[0].TargetPort != two[1].ListenPort || two[1].ID != hkFwd.ID {
		t.Fatalf("remove middle hop: %+v", two)
	}
	// 线路本身跟着变
	r = h.api("GET", "/api/v1/routes/hk", nil)
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), `"host":"gz-01"`) || strings.Contains(r.raw2(), "mid-01") {
		t.Fatalf("route view: %s", r.raw)
	}
}
