package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store/db"
)

func TestAllocateGroupSamePublicPort(t *testing.T) {
	la := machine(1, "bwh-la", `[{"local":"41000-41010"}]`, false)
	hk := machine(2, "hnyun-hk", `[{"local":"41000-41010"}]`, false)
	next := machine(3, "mid", `[{"local":"41000-41010"}]`, false)
	// 41000 在成员上被占用，41001 在中间跳上被占用：第一跳和成员同号 41002，中间跳也同号
	ports, peers, err := AllocateGroup([]Hop{hop(la), hop(next, 41001)}, []Hop{hop(hk, 41000)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Listen != 41002 || peers[0].Listen != 41002 || ports[1].Listen != 41002 {
		t.Fatalf("want same port 41002 everywhere: %+v %+v", ports, peers)
	}
	// 中间跳的端口段与第一跳不重叠时，第一跳和成员仍同号，中间跳另外分配
	other := machine(4, "other", `[{"local":"50000-50010"}]`, false)
	ports, peers, err = AllocateGroup([]Hop{hop(la), hop(other)}, []Hop{hop(hk)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Listen != peers[0].Listen || ports[1].Listen == ports[0].Listen {
		t.Fatalf("first hop and peer must share, middle hop falls back: %+v %+v", ports, peers)
	}
}

func TestAllocateGroupNATAlignsPublicPort(t *testing.T) {
	// 成员是 NAT：本机 20000 对应外网 41005；第一跳只有 41005 可用时成员用本机 20000
	first := machine(1, "a", `[{"local":"41005-41006"}]`, false)
	nat := machine(2, "nat", `[{"local":"20000-20001","public_from":41005}]`, true)
	ports, peers, err := AllocateGroup([]Hop{hop(first)}, []Hop{hop(nat)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Public != 41005 || peers[0].Listen != 20000 || peers[0].Public != 41005 {
		t.Fatalf("nat peer must align on public port: %+v %+v", ports, peers)
	}
	// 端口段不重叠：报错并指出机器
	far := machine(3, "far", `[{"local":"50000-50001"}]`, false)
	_, _, err = AllocateGroup([]Hop{hop(first)}, []Hop{hop(far)})
	if e := apierr.From(err); e.Code != "port_exhausted" || !strings.Contains(e.Message, "far") {
		t.Fatalf("want port_exhausted naming peer: %v", err)
	}
}

func TestPeerPort(t *testing.T) {
	m := machine(1, "hk", `[{"local":"63000-63010"}]`, false)
	if hp, err := PeerPort(hop(m), 63003); err != nil || hp.Listen != 63003 {
		t.Fatalf("peer port: %+v %v", hp, err)
	}
	if _, err := PeerPort(hop(m, 63003), 63003); apierr.From(err).Code != "port_taken" {
		t.Fatalf("taken: %v", err)
	}
	if _, err := PeerPort(hop(m), 41000); apierr.From(err).Code != "port_taken" {
		t.Fatalf("out of range: %v", err)
	}
}

func groupWorld(entryIPs []string, laState, hkState string, hkStatus string) (*World, db.Node, db.NodeEntry) {
	la := machine(1, "bwh-la", "", false)
	la.TolatoIp, la.State = "144.34.231.114", laState
	hk := machine(2, "hnyun-hk", "", false)
	hk.TolatoIp, hk.State = "177.201.228.146", hkState
	at := time.Now()
	route := db.Route{ID: 5, Code: "reality", Name: "Reality 入口", Hops: []int64{1}, EntryPeers: []int64{2},
		EntryAddress: "reality.example.com", EntryIps: entryIPs, EntryResolvedAt: &at}
	rid := route.ID
	fs := []db.Forward{
		{ID: 11, EntryID: 9, Hop: 0, MachineID: 1, ListenPort: 63000, PublicPort: 63000, Mode: "nft", Status: "active"},
		{ID: 12, EntryID: 9, Hop: 0, MachineID: 2, ListenPort: 63000, PublicPort: 63000, Mode: "nft", Status: hkStatus, Mirror: true},
	}
	n := db.Node{ID: 7, Name: "BAGE-SGS", Address: "85.149.211.121", Port: 54211, State: "online"}
	e := db.NodeEntry{ID: 9, NodeID: 7, RouteID: &rid, Enabled: true}
	return NewWorld([]db.Route{route}, []db.Machine{la, hk}, fs), n, e
}

func TestResolveEntryGroup(t *testing.T) {
	// 订阅地址用入口地址；镜像不算作一跳
	w, n, e := groupWorld([]string{"144.34.231.114"}, "online", "online", "active")
	ep := w.Resolve(n, e)
	if !ep.Available || ep.Host != "reality.example.com" || ep.Port != 63000 {
		t.Fatalf("group entry: %+v", ep)
	}
	if len(w.Forwards[9]) != 1 || len(w.Mirrors[9]) != 1 {
		t.Fatalf("mirrors must be kept apart: %+v %+v", w.Forwards[9], w.Mirrors[9])
	}

	// 域名指向 la：hk 离线不影响
	w, n, e = groupWorld([]string{"144.34.231.114"}, "online", "offline", "active")
	if ep := w.Resolve(n, e); !ep.Available {
		t.Fatalf("inactive peer offline must not withdraw: %+v", ep)
	}
	// 域名指向 la 而 la 离线：撤下，原因指出机器
	w, n, e = groupWorld([]string{"144.34.231.114"}, "offline", "online", "active")
	if ep := w.Resolve(n, e); ep.Available || !strings.Contains(ep.Reason, "bwh-la") {
		t.Fatalf("active member offline must withdraw: %+v", ep)
	}
	// 切到 hk 后恢复
	w, n, e = groupWorld([]string{"177.201.228.146"}, "offline", "online", "active")
	if ep := w.Resolve(n, e); !ep.Available {
		t.Fatalf("after DNS switch to hk: %+v", ep)
	}
	// 指向 hk 但 hk 上的镜像还没生效
	w, n, e = groupWorld([]string{"177.201.228.146"}, "online", "online", "pending")
	if ep := w.Resolve(n, e); ep.Available || !strings.Contains(ep.Reason, "hnyun-hk") {
		t.Fatalf("active mirror pending: %+v", ep)
	}
	// 两台都解析到（轮询）：两台都要可用
	w, n, e = groupWorld([]string{"144.34.231.114", "177.201.228.146"}, "online", "offline", "active")
	if ep := w.Resolve(n, e); ep.Available {
		t.Fatalf("round-robin needs every resolved member: %+v", ep)
	}
	// 解析结果未知或指向别处：任一成员可用即可
	for _, ips := range [][]string{nil, {"198.18.0.1"}} {
		w, n, e = groupWorld(ips, "offline", "online", "active")
		if ep := w.Resolve(n, e); !ep.Available {
			t.Fatalf("unknown resolution %v, any member: %+v", ips, ep)
		}
		w, n, e = groupWorld(ips, "offline", "offline", "active")
		if ep := w.Resolve(n, e); ep.Available {
			t.Fatalf("unknown resolution %v, none available: %+v", ips, ep)
		}
	}
	// 入口自己覆盖的地址优先于线路入口地址
	w, n, e = groupWorld(nil, "online", "online", "active")
	e.Address = "override.example.com"
	if ep := w.Resolve(n, e); ep.Host != "override.example.com" {
		t.Fatalf("entry override: %+v", ep)
	}
}

func TestActiveMembersIgnoresStaleResolution(t *testing.T) {
	// 解析结果指向已离线的 la，但已经过期：按不知道指向处理，hk 可用即可
	w, n, e := groupWorld([]string{"144.34.231.114"}, "offline", "online", "active")
	r := w.Routes[5]
	old := time.Now().Add(-EntryIPsTTL - time.Minute)
	r.EntryResolvedAt = &old
	w.Routes[5] = r
	if got := w.ActiveMembers(r); got != nil {
		t.Fatalf("stale resolution must be ignored: %v", got)
	}
	if ep := w.Resolve(n, e); !ep.Available {
		t.Fatalf("stale resolution falls back to any member: %+v", ep)
	}
}

func TestActiveMembersUsesPublicIP(t *testing.T) {
	w, _, _ := groupWorld([]string{"203.0.113.7"}, "online", "online", "active")
	m := w.Machines[2]
	m.PublicAddress = "203.0.113.7"
	w.Machines[2] = m
	if got := w.ActiveMembers(w.Routes[5]); len(got) != 1 || got[0] != 2 {
		t.Fatalf("active by public address IP: %v", got)
	}
}
