package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
)

// TestRouteEntryGroup 入口组：两台入口机器共用一个域名，域名解析到哪台都能连上。
// 建线路 → 加入口时两台同号分配端口、成员上建镜像转发 → 订阅用入口地址 → 两台 patato-node 各自同步到转发 →
// 域名指向 A 时 B 掉线不撤入口，A 掉线撤下，切到 B 后恢复 → 新加成员给已有入口补镜像 → 去掉成员为敏感并删除镜像。
func TestRouteEntryGroup(t *testing.T) {
	env := newRelayEnv(t)
	h := env.h
	ctx := context.Background()
	la, hk := env.gz, env.hk // 沿用两台已接入的机器，分别当作 bwh-la 和 hnyun-hk
	for _, host := range []string{"gz-01", "hk-01"} {
		expect(t, h.api("PATCH", "/api/v1/machines/"+host+"/network", map[string]any{
			"port_ranges": []map[string]any{{"local": "63000-63010"}},
		}), 200)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET tolato_ip = '144.34.231.114' WHERE host = 'gz-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET tolato_ip = '177.201.228.146' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}

	// 有成员时必须填入口地址；成员不能同时是一跳
	r := h.api("POST", "/api/v1/routes", map[string]any{"code": "reality", "name": "Reality", "hops": []string{"gz-01"}, "entry_peers": []string{"hk-01"}, "dry_run": true})
	if r.status != 400 || r.str("error.code") != "entry_address_required" {
		t.Fatalf("missing entry address: %s", r.raw)
	}
	r = h.api("POST", "/api/v1/routes", map[string]any{"code": "reality", "name": "Reality", "hops": []string{"gz-01"}, "entry_peers": []string{"gz-01"},
		"entry_address": "reality.example.com", "dry_run": true})
	if r.status != 400 || r.str("error.code") != "entry_peer_in_hops" {
		t.Fatalf("peer in hops: %s", r.raw)
	}
	expect(t, h.api("POST", "/api/v1/routes", map[string]any{"code": "reality", "name": "Reality", "hops": []string{"gz-01"},
		"entry_peers": []string{"hk-01"}, "entry_address": "reality.example.com"}), 200)

	// hk 上 63000 被占用：两台同号分到 63001
	if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET listening_ports = '{63000}' WHERE host = 'hk-01'`); err != nil {
		t.Fatal(err)
	}
	r = h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "reality", "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "gz-01:63001") || !strings.Contains(r.raw2(), "入口组 hk-01:63001") || !strings.Contains(r.raw2(), "reality.example.com:63001") {
		t.Fatalf("dry run: %s", r.raw)
	}
	r = h.api("POST", "/api/v1/changes/"+r.str("change_id")+":apply", nil)
	expect(t, r, 200)
	entries, _ := h.a.Store.ListNodeEntries(ctx, env.node.ID)
	entry := entries[len(entries)-1]
	fs, _ := h.a.Store.ListEntryForwards(ctx, entry.ID)
	mirrors, _ := h.a.Store.ListEntryMirrors(ctx, entry.ID)
	if len(fs) != 1 || len(mirrors) != 1 || fs[0].MachineID != la.ID || mirrors[0].MachineID != hk.ID ||
		mirrors[0].ListenPort != 63001 || mirrors[0].TargetHost != fs[0].TargetHost || mirrors[0].TargetPort != fs[0].TargetPort {
		t.Fatalf("forwards %+v mirrors %+v", fs, mirrors)
	}

	// 两台 patato-node 都同步到同一个端口的转发
	h.a.Native.SetResolver(fakeIPResolver{"la.example.com": "203.0.113.9", "reality.example.com": "144.34.231.114"})
	ls := env.sync(env.gzTok, "")
	hs := env.sync(env.hkTok, "")
	if ls.Forwards == nil || len(ls.Forwards.Items) != 1 || hs.Forwards == nil || len(hs.Forwards.Items) != 1 ||
		hs.Forwards.Items[0].ListenPort != 63001 || hs.Forwards.Items[0].TargetIP != "203.0.113.9" {
		t.Fatalf("sync la %+v hk %+v", ls.Forwards, hs.Forwards)
	}
	now := time.Now()
	report := func(tok, spool string, seq, id int64) {
		expect(t, env.machineDo(tok, nodeapi.PathReport, nodeapi.ReportRequest{Spool: spool, Seq: seq, At: now,
			Forwards: []nodeapi.ForwardReport{{ID: id, Up: 100, Down: 900, Applied: true, Mode: "nft", At: now}}}), 200)
	}
	report(env.gzTok, "la", 1, fs[0].ID)
	report(env.hkTok, "hk", 1, mirrors[0].ID)

	// 订阅用入口地址；入口流量含两台
	sub := env.sub(env.subTokens[0], "clash-verge/v1.7")
	if !strings.Contains(sub, "reality.example.com") || !strings.Contains(sub, "63001") {
		t.Fatalf("subscription must use entry address:\n%s", sub)
	}
	if n, err := h.a.Native.RefreshEntryAddresses(ctx); err != nil || n != 1 {
		t.Fatalf("refresh entry address: %d %v", n, err)
	}
	r = h.api("GET", "/api/v1/routes/reality", nil)
	expect(t, r, 200)
	if r.str("entry_ips.0") != "144.34.231.114" || r.str("hops.0.active") != "true" || r.str("entry_peers.0.host") != "hk-01" ||
		r.str("entries.0.today_up") != "200" || r.str("today_up") != "200" {
		t.Fatalf("route view: %s", r.raw)
	}

	// 域名指向 la：hk 掉线不撤入口，告警说明入口组
	setSeen := func(host string, ago string) {
		if _, err := h.a.Store.Pool.Exec(ctx, `UPDATE machines SET last_seen_at = now() - $2::interval WHERE host = $1`, host, ago); err != nil {
			t.Fatal(err)
		}
	}
	setSeen("gz-01", "0 seconds")
	setSeen("hk-01", "1 hour")
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "reality.example.com") {
		t.Fatal("inactive member offline must not withdraw the entry")
	}
	r = h.api("GET", "/api/v1/events", nil)
	if !strings.Contains(r.raw2(), "入口组") {
		t.Fatalf("hop offline event must mention entry group: %s", r.raw)
	}
	// la 掉线、hk 恢复：域名仍指向 la 时撤下；切到 hk 后恢复
	setSeen("gz-01", "1 hour")
	setSeen("hk-01", "0 seconds")
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "reality.example.com") {
		t.Fatal("active member offline must withdraw the entry")
	}
	h.a.Native.SetResolver(fakeIPResolver{"la.example.com": "203.0.113.9", "reality.example.com": "177.201.228.146"})
	if _, err := h.a.Native.RefreshEntryAddresses(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.sub(env.subTokens[0], "clash-verge/v1.7"), "reality.example.com") {
		t.Fatal("entry must come back after DNS switches to the healthy member")
	}
	setSeen("gz-01", "0 seconds")
	if err := h.a.Relay.CheckHops(ctx); err != nil {
		t.Fatal(err)
	}

	// 新加成员：给已有入口补同端口的镜像；没接入 patato-node 的机器不能加
	expect(t, h.api("PATCH", "/api/v1/machines/cn-99/network", map[string]any{"public_address": "198.51.100.99"}), 200)
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01", "cn-99"}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "machine_not_enrolled" {
		t.Fatalf("unenrolled peer: %s", r.raw)
	}
	third, thirdTok := enrollMachine(t, h, "sg-01")
	expect(t, h.api("PATCH", "/api/v1/machines/sg-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "63000-63010"}}}), 200)
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01", "sg-01"}, "dry_run": true})
	expect(t, r, 200)
	if r.str("risk") != "normal" || !strings.Contains(r.raw2(), "sg-01") || !strings.Contains(r.raw2(), "63001") {
		t.Fatalf("add peer dry run: %s", r.raw)
	}
	r = h.api("POST", "/api/v1/changes/"+r.str("change_id")+":apply", nil)
	expect(t, r, 200)
	mirrors, _ = h.a.Store.ListEntryMirrors(ctx, entry.ID)
	if len(mirrors) != 2 || mirrors[1].MachineID != third.ID || mirrors[1].ListenPort != 63001 {
		t.Fatalf("mirror for new peer: %+v", mirrors)
	}
	if ss := env.sync(thirdTok, ""); ss.Forwards == nil || len(ss.Forwards.Items) != 1 || ss.Forwards.Items[0].ListenPort != 63001 {
		t.Fatalf("new peer sync: %+v", ss.Forwards)
	}

	// 预演之后新加的入口：执行时按当前入口重新对齐，也拿到新成员上的镜像
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01", "sg-01", "nl-01"}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "machine_not_enrolled" {
		t.Fatalf("unknown peer must be refused while entries exist: %s", r.raw)
	}
	enrollMachine(t, h, "nl-01")
	expect(t, h.api("PATCH", "/api/v1/machines/nl-01/network", map[string]any{"port_ranges": []map[string]any{{"local": "63000-63010"}}}), 200)
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01", "sg-01", "nl-01"}, "dry_run": true})
	expect(t, r, 200)
	pending := r.str("change_id")
	expect(t, h.api("POST", "/api/v1/nodes/la-01/entries", map[string]any{"route": "reality", "suffix": "备用"}), 200)
	all, _ := h.a.Store.ListNodeEntries(ctx, env.node.ID)
	late := all[len(all)-1]
	expect(t, h.api("POST", "/api/v1/changes/"+pending+":apply", nil), 200)
	lm, _ := h.a.Store.ListEntryMirrors(ctx, late.ID)
	if len(lm) != 3 {
		t.Fatalf("entry added after the plan must get mirrors on every member: %+v", lm)
	}
	// 漏掉的镜像可以用同样的成员列表补齐
	if _, err := h.a.Store.Pool.Exec(ctx, `DELETE FROM forwards WHERE id = $1`, lm[2].ID); err != nil {
		t.Fatal(err)
	}
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01", "sg-01", "nl-01"}, "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "补齐") {
		t.Fatalf("repair plan: %s", r.raw)
	}
	expect(t, h.api("POST", "/api/v1/changes/"+r.str("change_id")+":apply", nil), 200)
	if lm, _ = h.a.Store.ListEntryMirrors(ctx, late.ID); len(lm) != 3 {
		t.Fatalf("resubmitting the same members must repair missing mirrors: %+v", lm)
	}
	r = h.api("DELETE", fmt.Sprintf("/api/v1/nodes/la-01/entries/%d", late.ID), map[string]any{"dry_run": true})
	expect(t, r, 200)
	approveChange(t, h, r.str("change_id"))

	// 去掉成员是敏感操作，删除它上面的镜像
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"entry_peers": []string{"hk-01"}, "dry_run": true})
	expect(t, r, 200)
	if r.str("risk") != "sensitive" {
		t.Fatalf("removing a peer must be sensitive: %s", r.raw)
	}
	approveChange(t, h, r.str("change_id"))
	mirrors, _ = h.a.Store.ListEntryMirrors(ctx, entry.ID)
	if len(mirrors) != 1 || mirrors[0].MachineID != hk.ID {
		t.Fatalf("mirror removed: %+v", mirrors)
	}

	// 改跳和改入口组要分开；有入口时不能换第一跳
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"hops": []string{"sg-01"}, "entry_peers": []string{}, "dry_run": true})
	if r.status != 400 || r.str("error.code") != "split_update" {
		t.Fatalf("split update: %s", r.raw)
	}
	r = h.api("PATCH", "/api/v1/routes/reality", map[string]any{"hops": []string{"sg-01"}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "entry_group_first_hop" {
		t.Fatalf("first hop change with group: %s", r.raw)
	}

	// 改成员的端口映射会让入口组对外端口对不上：拒绝
	r = h.api("PATCH", "/api/v1/machines/hk-01/network", map[string]any{"nat": true,
		"port_ranges": []map[string]any{{"local": "63000-63010", "public_from": 30000}}, "dry_run": true})
	if r.status != 409 || r.str("error.code") != "entry_group_port_mismatch" {
		t.Fatalf("port mapping change on member: %s", r.raw)
	}

	// 删除入口时一起删除镜像
	r = h.api("DELETE", fmt.Sprintf("/api/v1/nodes/la-01/entries/%d", entry.ID), map[string]any{"dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(r.raw2(), "释放 2 条转发") {
		t.Fatalf("delete counts mirrors: %s", r.raw)
	}
	approveChange(t, h, r.str("change_id"))
	if ss := env.sync(env.hkTok, hs.Forwards.Rev); ss.Forwards == nil || len(ss.Forwards.Items) != 0 {
		t.Fatalf("mirror must be removed with the entry: %+v", ss.Forwards)
	}
}
