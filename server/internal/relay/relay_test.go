package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store/db"
)

func machine(id int64, host string, ranges string, nat bool) db.Machine {
	if ranges == "" {
		ranges = "[]"
	}
	return db.Machine{ID: id, Host: host, PortRanges: json.RawMessage(ranges), ReservedPorts: json.RawMessage("[]"), Nat: nat, State: "online"}
}

func TestParseRanges(t *testing.T) {
	rs, err := ParseRanges(json.RawMessage(`[{"local":"20000-20099","public_from":30000},"41000"]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].From != 20000 || rs[0].To != 20099 || rs[0].Public(20005) != 30005 || rs[1].From != 41000 || rs[1].To != 41000 {
		t.Fatalf("ranges: %+v", rs)
	}
	b, _ := json.Marshal(rs)
	if string(b) != `[{"local":"20000-20099","public_from":30000},{"local":"41000"}]` {
		t.Fatalf("marshal: %s", b)
	}
	for _, bad := range []string{`[{"local":"5-1"}]`, `[{"local":"1-70000"}]`, `[{"local":"x"}]`,
		`[{"local":"100-200"},{"local":"150-300"}]`, `[{"local":"65000-65535","public_from":65530}]`} {
		if _, err := ParseRanges(json.RawMessage(bad)); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}

func TestRangesDefaultAndNAT(t *testing.T) {
	if rs := Ranges(machine(1, "a", "", false)); len(rs) != 1 || rs[0] != DefaultRange {
		t.Fatalf("default: %+v", rs)
	}
	if rs := Ranges(machine(1, "a", "", true)); len(rs) != 0 {
		t.Fatalf("nat without ranges must have none: %+v", rs)
	}
	m := machine(1, "a", `[{"local":"20000-20009","public_from":30000}]`, true)
	if PublicPort(m, 20003) != 30003 || PublicPort(m, 41000) != 41000 {
		t.Fatal("public port mapping")
	}
}

func hop(m db.Machine, used ...int) Hop {
	o := NewOccupied()
	for _, p := range used {
		o.Add(p, "test")
	}
	return Hop{Machine: m, Occupied: o}
}

func TestAllocateSamePortAcrossHops(t *testing.T) {
	a := machine(1, "gz", `[{"local":"41000-41010"}]`, false)
	b := machine(2, "hk", `[{"local":"41000-41010"}]`, false)
	// 41000 在 A 上被占用，41001 在 B 上被占用：第一个两边都空闲的是 41002
	ports, err := Allocate([]Hop{hop(a, 41000), hop(b, 41001)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Listen != 41002 || ports[1].Listen != 41002 {
		t.Fatalf("want same port 41002 on both hops, got %+v", ports)
	}
}

func TestAllocateFallsBackToPerHop(t *testing.T) {
	a := machine(1, "gz", `[{"local":"41000-41001"}]`, false)
	b := machine(2, "hk", `[{"local":"42000-42001"}]`, false)
	ports, err := Allocate([]Hop{hop(a, 41000), hop(b)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Listen != 41001 || ports[1].Listen != 42000 {
		t.Fatalf("per-hop allocation: %+v", ports)
	}
}

func TestAllocateNATPublicPort(t *testing.T) {
	a := machine(1, "nat", `[{"local":"20000-20009","public_from":30000}]`, true)
	ports, err := Allocate([]Hop{hop(a, 20000)})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].Listen != 20001 || ports[0].Public != 30001 {
		t.Fatalf("nat: %+v", ports)
	}
}

func TestAllocateAvoidsReservedAndRecordsAllocation(t *testing.T) {
	a := machine(1, "gz", `[{"local":"41000-41003"}]`, false)
	h := hop(a)
	h.Occupied.Reserve(PortRange{From: 41000, To: 41001})
	p1, err := Allocate([]Hop{h})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Allocate([]Hop{h})
	if err != nil {
		t.Fatal(err)
	}
	if p1[0].Listen != 41002 || p2[0].Listen != 41003 {
		t.Fatalf("reserved / sequential: %v %v", p1, p2)
	}
	_, err = Allocate([]Hop{h})
	if e := apierr.From(err); e.Code != "port_exhausted" || !strings.Contains(e.Message, "gz") || !strings.Contains(e.Message, "41000-41003") {
		t.Fatalf("want port_exhausted naming machine and range, got %v", err)
	}
	if err := Check(h, 41002); apierr.From(err).Code != "port_taken" {
		t.Fatalf("check: %v", err)
	}
}

func TestAllocateNATWithoutRanges(t *testing.T) {
	_, err := Allocate([]Hop{hop(machine(1, "nat", "", true))})
	if apierr.From(err).Code != "port_exhausted" {
		t.Fatalf("want port_exhausted, got %v", err)
	}
}

func TestTargets(t *testing.T) {
	a := machine(1, "gz", "", false)
	a.TolatoIp = "198.51.100.1"
	b := machine(2, "hk", `[{"local":"20000-20009","public_from":30000}]`, true)
	b.PublicAddress = "hk.example.com"
	landing := machine(3, "la", "", false)
	landing.TolatoIp = "203.0.113.9"
	n := db.Node{ID: 7, Name: "洛杉矶", Address: "la.example.com", Port: 443, ServerPort: 8443}
	ports := []HopPorts{{Listen: 41000, Public: 41000}, {Listen: 20001, Public: 30001}}
	ts := Targets(n, &landing, []db.Machine{a, b}, ports)
	if ts[0] != (Target{Host: "hk.example.com", Port: 30001}) {
		t.Fatalf("middle hop must target next machine's public address and public port: %+v", ts[0])
	}
	if ts[1] != (Target{Host: "203.0.113.9", Port: 443}) {
		t.Fatalf("last hop must target landing machine IP and node public port: %+v", ts[1])
	}
	if got := Targets(n, nil, []db.Machine{a}, ports[:1]); got[0].Host != "la.example.com" {
		t.Fatalf("without landing machine use node address: %+v", got)
	}
}

func TestResolveAvailability(t *testing.T) {
	gz := machine(1, "gz", "", false)
	gz.PublicAddress = "gz.example.com"
	hk := machine(2, "hk", "", false)
	route := db.Route{ID: 5, Code: "gz-hk", Name: "广州中转", Hops: []int64{1, 2}}
	rid := route.ID
	fs := []db.Forward{
		{ID: 11, EntryID: 9, Hop: 0, MachineID: 1, ListenPort: 41000, PublicPort: 41000, Mode: "nft", Status: "active"},
		{ID: 12, EntryID: 9, Hop: 1, MachineID: 2, ListenPort: 41000, PublicPort: 41000, Mode: "nft", Status: "active"},
	}
	n := db.Node{ID: 7, Name: "洛杉矶", Address: "la.example.com", Port: 443, State: "online"}
	e := db.NodeEntry{ID: 9, NodeID: 7, RouteID: &rid, Enabled: true}
	w := NewWorld([]db.Route{route}, []db.Machine{gz, hk}, fs)
	ep := w.Resolve(n, e)
	if !ep.Available || ep.Host != "gz.example.com" || ep.Port != 41000 || ep.Name != "洛杉矶 · 广州中转" {
		t.Fatalf("available entry: %+v", ep)
	}

	hk.State = "offline"
	w = NewWorld([]db.Route{route}, []db.Machine{gz, hk}, fs)
	if ep := w.Resolve(n, e); ep.Available || !strings.Contains(ep.Reason, "hk") {
		t.Fatalf("hop offline must make entry unavailable: %+v", ep)
	}

	hk.State = "unknown"
	fs[1].Status = "pending"
	w = NewWorld([]db.Route{route}, []db.Machine{gz, hk}, fs)
	if ep := w.Resolve(n, e); ep.Available {
		t.Fatalf("pending forward: %+v", ep)
	}

	fs[1].Status, fs[1].ProbeFailures = "active", 3
	w = NewWorld([]db.Route{route}, []db.Machine{gz, hk}, fs)
	if ep := w.Resolve(n, e); ep.Available || !strings.Contains(ep.Reason, "探测") {
		t.Fatalf("3 probe failures: %+v", ep)
	}

	// 手写转发没有生效状态，只看机器在线
	fs[0].Mode, fs[0].Status, fs[1].Mode, fs[1].Status, fs[1].ProbeFailures = "manual", "pending", "manual", "pending", 0
	w = NewWorld([]db.Route{route}, []db.Machine{gz, hk}, fs)
	if ep := w.Resolve(n, e); !ep.Available {
		t.Fatalf("manual forwards follow machine state only: %+v", ep)
	}

	n.State = "offline"
	if ep := w.Resolve(n, e); ep.Available {
		t.Fatal("offline node")
	}

	// 自定义入口：地址与端口覆盖，状态只跟随节点
	n.State = "online"
	custom := db.NodeEntry{ID: 10, NodeID: 7, Name: "洛杉矶 IPLC", Address: "iplc.example.com", Port: 2000, Enabled: true}
	if ep := w.Resolve(n, custom); !ep.Available || ep.Host != "iplc.example.com" || ep.Port != 2000 || ep.Name != "洛杉矶 IPLC" {
		t.Fatalf("custom: %+v", ep)
	}
}

func TestEndpointsImplicitAndDisabled(t *testing.T) {
	w := NewWorld(nil, nil, nil)
	n := db.Node{ID: 1, Name: "东京", Address: "jp.example.com", Port: 443, State: "online"}
	eps := w.Endpoints(n, nil)
	if len(eps) != 1 || eps[0].Host != "jp.example.com" || eps[0].Name != "东京" {
		t.Fatalf("implicit direct entry: %+v", eps)
	}
	eps = w.Endpoints(n, []db.NodeEntry{{ID: 1, NodeID: 1, Enabled: false}, {ID: 2, NodeID: 1, Suffix: "备用", Address: "b.example.com", Enabled: true}})
	if len(eps) != 1 || eps[0].Name != "东京 · 备用" {
		t.Fatalf("disabled entries are skipped: %+v", eps)
	}
}

func TestHopState(t *testing.T) {
	now := time.Now()
	recent, old := now.Add(-time.Minute), now.Add(-time.Hour)
	on, off := true, false
	cases := []struct {
		m    db.Machine
		want string
	}{
		{db.Machine{TokenHash: []byte{1}, LastSeenAt: &recent}, "online"},
		{db.Machine{TokenHash: []byte{1}, LastSeenAt: &old}, "offline"},
		{db.Machine{TokenHash: []byte{1}}, "offline"},
		{db.Machine{TolatoOnline: &on}, "online"},
		{db.Machine{TolatoOnline: &off}, "offline"},
		{db.Machine{}, "unknown"},
	}
	for i, c := range cases {
		if got := HopState(c.m, now, 5*time.Minute); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

// 链路发现：广州（整段 DNAT 到香港）→ 香港（socat 每端口一个）→ 落地。
func TestTraceRoute(t *testing.T) {
	gz := ParseSnapshot("gz", `### addr
198.51.100.1/24
### iptables
*nat
-A PREROUTING -p tcp -m tcp --dport 60000:62000 -j DNAT --to-destination 198.51.100.2
-A PREROUTING -p udp -m udp --dport 60000:62000 -j DNAT --to-destination 198.51.100.2
COMMIT
### nft
### socat
`)
	hk := ParseSnapshot("hk", `### addr
198.51.100.2/24
### iptables
### nft
### socat
socat TCP4-LISTEN:60001,reuseaddr,fork TCP4:la.example.com:443
socat UDP4-LISTEN:60001,reuseaddr,fork UDP4:la.example.com:443
`)
	if len(gz.Rules) != 2 || gz.Rules[0].From != 60000 || gz.Rules[0].To != 62000 || gz.Rules[0].TargetHost != "198.51.100.2" {
		t.Fatalf("iptables parse: %+v", gz.Rules)
	}
	if len(hk.Rules) != 2 || hk.Rules[0].From != 60001 || hk.Rules[0].TargetHost != "la.example.com" || hk.Rules[0].TargetPort != 443 {
		t.Fatalf("socat parse: %+v", hk.Rules)
	}
	res := fakeResolver{"gz.example.com": {"198.51.100.1"}, "la.example.com": {"203.0.113.9"}}
	tr := TraceRoute(context.Background(), res, "gz.example.com", 60001, []Snapshot{gz, hk}, nil,
		Landing{Addrs: []string{"la.example.com"}, Ports: []int{443}})
	if !tr.Complete || len(tr.Hops) != 2 || tr.Hops[0].Machine != "gz" || tr.Hops[0].TargetPort != 60001 ||
		tr.Hops[1].Machine != "hk" || tr.Hops[1].TargetHost != "la.example.com" {
		t.Fatalf("trace: %+v", tr)
	}
	if r := tr.Reserved["gz"]; len(r) == 0 || r[0].From != 60000 || r[0].To != 62000 {
		t.Fatalf("whole-range DNAT must be reported as reserved: %+v", tr.Reserved)
	}

	tr = TraceRoute(context.Background(), res, "gz.example.com", 60002, []Snapshot{gz, hk}, nil,
		Landing{Addrs: []string{"la.example.com"}, Ports: []int{443}})
	if tr.Complete || !strings.Contains(tr.Problem, "hk") {
		t.Fatalf("missing socat must stop at hk: %+v", tr)
	}
}

func TestParseNftDnat(t *testing.T) {
	s := ParseSnapshot("c", "### nft\n\t\ttcp dport 63001 dnat ip to 203.0.113.5:443\n")
	if len(s.Rules) != 1 || s.Rules[0].From != 63001 || s.Rules[0].TargetHost != "203.0.113.5" || s.Rules[0].TargetPort != 443 {
		t.Fatalf("nft: %+v", s.Rules)
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return f[host], nil
}
