package relay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/momaek/patato/nodeapi"
)

// TestBuildRuleset 逐条对比生成的规则集与预期的 nft 规则。
func TestBuildRuleset(t *testing.T) {
	rs, skipped := Build([]nodeapi.Forward{
		{ID: 13, ListenPort: 41011, TargetIP: "192.0.2.8", TargetPort: 443},
		{ID: 12, ListenPort: 41010, TargetIP: "192.0.2.7", TargetPort: 41010},
		{ID: 14, ListenPort: 41012, TargetIP: "2001:db8::1", TargetPort: 443},
	})
	want := []string{
		"meta nfproto ipv4 tcp dport 41010 ct mark set 12 dnat ip to 192.0.2.7:41010",
		"meta nfproto ipv4 udp dport 41010 ct mark set 12 dnat ip to 192.0.2.7:41010",
		"ct mark 12 masquerade",
		"ct mark 12 ct direction original counter name f12_up",
		"ct mark 12 ct direction reply counter name f12_down",
		"meta nfproto ipv4 tcp dport 41011 ct mark set 13 dnat ip to 192.0.2.8:443",
		"meta nfproto ipv4 udp dport 41011 ct mark set 13 dnat ip to 192.0.2.8:443",
		"ct mark 13 masquerade",
		"ct mark 13 ct direction original counter name f13_up",
		"ct mark 13 ct direction reply counter name f13_down",
	}
	if len(rs.Rules) != len(want) {
		t.Fatalf("got %d rules, want %d:\n%s", len(rs.Rules), len(want), rs.Render())
	}
	for i, r := range rs.Rules {
		if r.String() != want[i] {
			t.Errorf("rule %d:\n got  %s\n want %s", i, r, want[i])
		}
	}
	wantChains := []Chain{ChainPre, ChainPre, ChainPost, ChainFwd, ChainFwd}
	for i, r := range rs.Rules[:5] {
		if r.Chain != wantChains[i] {
			t.Errorf("rule %d in chain %s, want %s", i, r.Chain, wantChains[i])
		}
	}
	if strings.Join(rs.Counters, ",") != "f12_up,f12_down,f13_up,f13_down" {
		t.Fatalf("counters: %v", rs.Counters)
	}
	if skipped[14] == nil || skipped[12] != nil {
		t.Fatalf("IPv6 next hop must be skipped for nft: %v", skipped)
	}
	out := rs.Render()
	for _, s := range []string{"table inet patato {", "type nat hook prerouting priority dstnat; policy accept;",
		"type nat hook postrouting priority srcnat; policy accept;", "type filter hook forward priority filter; policy accept;",
		"counter f12_up {}"} {
		if !strings.Contains(out, s) {
			t.Errorf("render missing %q:\n%s", s, out)
		}
	}
	empty, _ := Build(nil)
	if len(empty.Rules) != 0 || len(empty.Counters) != 0 {
		t.Fatal("empty list must produce empty ruleset")
	}
}

func TestDelta(t *testing.T) {
	cases := []struct {
		prev, cur uint64
		want      int64
	}{
		{0, 100, 100},
		{100, 250, 150},
		{250, 250, 0},
		{250, 40, 40}, // 读数变小：重启或表重建，视为归零后从新读数累计
	}
	for _, c := range cases {
		if got := Delta(c.prev, c.cur); got != c.want {
			t.Errorf("Delta(%d, %d) = %d, want %d", c.prev, c.cur, got, c.want)
		}
	}
}

func TestParseCounter(t *testing.T) {
	if id, dir, ok := parseCounter("f42_down"); !ok || id != 42 || dir != "down" {
		t.Fatal("f42_down")
	}
	for _, bad := range []string{"x1_up", "f_up", "f1_side", "f1"} {
		if _, _, ok := parseCounter(bad); ok {
			t.Errorf("%s should not parse", bad)
		}
	}
}

func TestParseProcNet(t *testing.T) {
	in := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:A0E2 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1
   1: 0100007F:1F90 0100007F:D3C2 01 00000000:00000000 00:00000000 00000000     0        0 2
   2: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3
`
	got := parseProcNetLines(bufio.NewScanner(strings.NewReader(in)), true)
	if len(got) != 2 || got[0] != 41186 || got[1] != 22 {
		t.Fatalf("listening tcp: %v", got)
	}
	if got := parseProcNetLines(bufio.NewScanner(strings.NewReader(in)), false); len(got) != 3 {
		t.Fatalf("udp (all bound): %v", got)
	}
}

func TestParseTag(t *testing.T) {
	if id, ok := ParseTag("r17"); !ok || id != 17 {
		t.Fatal("r17")
	}
	if _, ok := ParseTag("n17"); ok {
		t.Fatal("node inbound tag must not parse as relay")
	}
}

// fakeBackend 记录 Apply 的列表，Read 返回预设计数。
type fakeBackend struct {
	name    string
	applied []nodeapi.Forward
	counts  map[int64]Counters
	fail    error
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Apply(fs []nodeapi.Forward) map[int64]error {
	f.applied = fs
	out := map[int64]error{}
	for _, x := range fs {
		out[x.ID] = f.fail
	}
	return out
}
func (f *fakeBackend) Read() (map[int64]Counters, error) { return f.counts, nil }
func (f *fakeBackend) Close() error                      { return nil }

func byID(rs []nodeapi.ForwardReport) map[int64]nodeapi.ForwardReport {
	out := map[int64]nodeapi.ForwardReport{}
	for _, r := range rs {
		out[r.ID] = r
	}
	return out
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func forwards(items ...nodeapi.Forward) *nodeapi.ForwardSync {
	return &nodeapi.ForwardSync{Rev: nodeapi.Rev(items), Items: items}
}

func TestManagerCountersAndReset(t *testing.T) {
	nft := &fakeBackend{name: "nft", counts: map[int64]Counters{}}
	m := NewManager(func() (Backend, error) { return nft, nil }, &fakeBackend{name: "userspace"}, quiet())
	m.Sync(forwards(nodeapi.Forward{ID: 1, ListenPort: 41000, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"}))
	if len(nft.applied) != 1 {
		t.Fatalf("nft should get the forward: %+v", nft.applied)
	}
	nft.counts[1] = Counters{Up: 1000, Down: 5000}
	r := m.Collect()
	if len(r) != 1 || r[0].Up != 1000 || r[0].Down != 5000 || !r[0].Applied || r[0].Mode != "nft" {
		t.Fatalf("first collect: %+v", r)
	}
	nft.counts[1] = Counters{Up: 1500, Down: 5000}
	if r := m.Collect(); r[0].Up != 500 || r[0].Down != 0 {
		t.Fatalf("delta: %+v", r)
	}
	// 表被重建：读数变小，从新读数累计，不出现负数也不重复计算
	nft.counts[1] = Counters{Up: 200, Down: 300}
	if r := m.Collect(); r[0].Up != 200 || r[0].Down != 300 {
		t.Fatalf("reset: %+v", r)
	}
	// 重新同步会整表重建：替换前读一次，之后计数器从 0 开始；重建后涨过旧读数也不会少算
	nft.counts[1] = Counters{Up: 250, Down: 300}
	m.Sync(forwards(nodeapi.Forward{ID: 1, ListenPort: 41000, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"},
		nodeapi.Forward{ID: 5, ListenPort: 41005, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"}))
	nft.counts[1] = Counters{Up: 400, Down: 0}
	if r := byID(m.Collect()); r[1].Up != 50+400 || r[1].Down != 0 {
		t.Fatalf("rebuild: %+v", r[1])
	}
	nft.counts[1] = Counters{Up: 0, Down: 0}
	m.Sync(forwards(nodeapi.Forward{ID: 1, ListenPort: 41000, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"}))
	m.Collect()
	nft.counts[1] = Counters{Up: 200, Down: 300}
	m.Collect()
	// 删除转发：最后一段流量仍然上报一次，之后不再出现
	nft.counts[1] = Counters{Up: 260, Down: 300}
	m.Sync(forwards())
	r = m.Collect()
	if len(r) != 1 || r[0].Up != 60 || r[0].Applied {
		t.Fatalf("removed forward last traffic: %+v", r)
	}
	if r := m.Collect(); len(r) != 0 {
		t.Fatalf("removed forward must disappear: %+v", r)
	}
}

func TestManagerFallback(t *testing.T) {
	user := &fakeBackend{name: "userspace", counts: map[int64]Counters{}}
	// 没有 nftables：auto 模式全部退回用户态
	m := NewManager(func() (Backend, error) { return nil, ErrUnavailable }, user, quiet())
	m.Sync(forwards(nodeapi.Forward{ID: 1, ListenPort: 41000, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"}))
	if len(user.applied) != 1 {
		t.Fatalf("auto mode without nftables must fall back to userspace: %+v", user.applied)
	}
	if st := m.Status(); st.Mode != "userspace" {
		t.Fatalf("status: %+v", st)
	}

	// 前置条件不满足（ip_forward 没开）：不偷偷退回，转发失败并上报原因
	user = &fakeBackend{name: "userspace"}
	m = NewManager(func() (Backend, error) { return nil, errors.New("net.ipv4.ip_forward 没有开启") }, user, quiet())
	m.Sync(forwards(nodeapi.Forward{ID: 2, ListenPort: 41001, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"}))
	r := m.Collect()
	if len(user.applied) != 0 || len(r) != 1 || r[0].Applied || !strings.Contains(r[0].Error, "ip_forward") {
		t.Fatalf("prerequisite failure must be reported, not bypassed: applied=%v report=%+v", user.applied, r)
	}
	if st := m.Status(); st.Mode != "nft" || !strings.Contains(st.Error, "ip_forward") {
		t.Fatalf("status: %+v", st)
	}

	// IPv6 下一跳和机器指定 userspace 都走用户态
	nft := &fakeBackend{name: "nft"}
	user = &fakeBackend{name: "userspace"}
	m = NewManager(func() (Backend, error) { return nft, nil }, user, quiet())
	m.Sync(forwards(
		nodeapi.Forward{ID: 3, ListenPort: 41002, TargetIP: "192.0.2.1", TargetPort: 443, Mode: "nft"},
		nodeapi.Forward{ID: 4, ListenPort: 41003, TargetIP: "2001:db8::1", TargetPort: 443, Mode: "nft"},
	))
	if len(nft.applied) != 1 || nft.applied[0].ID != 3 || len(user.applied) != 1 || user.applied[0].ID != 4 {
		t.Fatalf("split: nft=%+v user=%+v", nft.applied, user.applied)
	}
	m.SetMode("userspace")
	if len(nft.applied) != 0 || len(user.applied) != 2 {
		t.Fatalf("userspace mode: nft=%+v user=%+v", nft.applied, user.applied)
	}
}

func TestManagerProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadPort := dead.Addr().(*net.TCPAddr).Port
	dead.Close()

	m := NewManager(func() (Backend, error) { return &fakeBackend{name: "nft"}, nil }, nil, quiet())
	m.Sync(forwards(
		nodeapi.Forward{ID: 1, ListenPort: 41000, TargetIP: "127.0.0.1", TargetPort: port},
		nodeapi.Forward{ID: 2, ListenPort: 41001, TargetIP: "127.0.0.1", TargetPort: deadPort},
	))
	for i := 0; i < 3; i++ {
		m.Probe(context.Background())
	}
	byID := map[int64]nodeapi.ForwardReport{}
	for _, r := range m.Collect() {
		byID[r.ID] = r
	}
	if p := byID[1].Probe; p == nil || p.OK != 3 || p.Fail != 0 || !p.Last || p.Consecutive != 0 || p.AvgMS < 1 {
		t.Fatalf("reachable: %+v", p)
	}
	if p := byID[2].Probe; p == nil || p.Fail != 3 || p.Consecutive != 3 || p.Last {
		t.Fatalf("unreachable: %+v", p)
	}
	// 连续失败次数跨上报周期保留
	m.Probe(context.Background())
	for _, r := range m.Collect() {
		if r.ID == 2 && (r.Probe.Consecutive != 4 || r.Probe.Fail != 1) {
			t.Fatalf("consecutive across periods: %+v", r.Probe)
		}
	}
}
