package meter

import (
	"fmt"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
)

func TestClassify(t *testing.T) {
	cases := map[string][2]string{
		"www.google.com":            {"domain", "google.com"},
		"WWW.Google.COM.":           {"domain", "google.com"},
		"a.b.example.co.uk":         {"domain", "example.co.uk"},
		"foo.github.io":             {"domain", "foo.github.io"}, // 私有后缀也按可注册域名处理
		"api.example.com:443":       {"domain", "example.com"},
		"localhost":                 {"domain", "localhost"},
		"1.2.3.4":                   {"ip", "1.2.3.4"},
		"::ffff:1.2.3.4":            {"ip", "1.2.3.4"},
		"[2001:db8::1]:443":         {"ip", "2001:db8::1"},
		"2001:DB8::1":               {"ip", "2001:db8::1"},
		"":                          {"", ""},
		"   ":                       {"", ""},
		"xn--fiqs8s.xn--fiqz9s":     {"domain", "xn--fiqs8s.xn--fiqz9s"},
		"cdn.xn--fiqs8s.xn--fiqz9s": {"domain", "xn--fiqs8s.xn--fiqz9s"},
	}
	for in, want := range cases {
		kind, dest := Classify(in)
		if kind != want[0] || dest != want[1] {
			t.Errorf("Classify(%q) = %s %s, want %s %s", in, kind, dest, want[0], want[1])
		}
	}
}

func findDest(r *nodeapi.NodeReport, user int64, dest string) *nodeapi.DestinationStat {
	if r == nil {
		return nil
	}
	for i := range r.Destinations {
		if r.Destinations[i].User == user && r.Destinations[i].Dest == dest {
			return &r.Destinations[i]
		}
	}
	return nil
}

func TestDestinationsTCPAndUDP(t *testing.T) {
	m := New(time.Minute)
	m.SetUsers(1, []nodeapi.User{{ID: 7}, {ID: 8}})
	m.SetDestinations(true)

	// TCP：建立时确定目标，读写同时计入用户和目标
	a, _ := m.Open(1, 7, ip("1.1.1.1"), nil)
	a.SetDestination(Classify("www.google.com"))
	a.Up(100)
	a.Down(1000)
	b, _ := m.Open(1, 7, ip("1.1.1.1"), nil)
	b.SetDestination(Classify("mail.google.com"))
	b.Down(500)
	c, _ := m.Open(1, 8, ip("2.2.2.2"), nil)
	c.SetDestination(Classify("8.8.8.8"))
	c.Up(10)

	// UDP：按每个包的目标累加，同一会话首次出现的目标算一个连接
	u, _ := m.Open(1, 8, ip("2.2.2.2"), nil)
	k1, d1 := Classify("1.0.0.1")
	k2, d2 := Classify("quic.example.com")
	u.UpTo(30, k1, d1)
	u.DownFrom(300, k1, d1)
	u.UpTo(40, k2, d2)
	u.UpTo(40, k2, d2)

	traffic := m.Collect()[1]
	if traffic.Traffic[7] != [2]int64{100, 1500} || traffic.Traffic[8] != [2]int64{120, 300} {
		t.Fatalf("user traffic changed: %+v", traffic.Traffic)
	}
	r := m.CollectDestinations()[1]
	if g := findDest(r, 7, "google.com"); g == nil || g.Up != 100 || g.Down != 1500 || g.Conns != 2 || g.Kind != "domain" {
		t.Fatalf("google: %+v", g)
	}
	if g := findDest(r, 8, "8.8.8.8"); g == nil || g.Up != 10 || g.Conns != 1 || g.Kind != "ip" {
		t.Fatalf("8.8.8.8: %+v", g)
	}
	if g := findDest(r, 8, "1.0.0.1"); g == nil || g.Up != 30 || g.Down != 300 || g.Conns != 1 {
		t.Fatalf("udp ip: %+v", g)
	}
	if g := findDest(r, 8, "example.com"); g == nil || g.Up != 80 || g.Conns != 1 {
		t.Fatalf("udp domain: %+v", g)
	}

	// 清零后：活跃连接继续计入同一条目，连接数不重复计算
	a.Up(5)
	r = m.CollectDestinations()[1]
	if g := findDest(r, 7, "google.com"); g == nil || g.Up != 5 || g.Conns != 0 {
		t.Fatalf("second period: %+v", g)
	}
	for _, s := range []*Session{a, b, c, u} {
		s.Close()
	}
	if r := m.CollectDestinations()[1]; r != nil {
		t.Fatalf("idle period reported: %+v", r)
	}
	if len(m.dest.counters) != 0 || len(m.dest.perNode) != 0 {
		t.Fatalf("closed entries not released: %d %v", len(m.dest.counters), m.dest.perNode)
	}
}

func TestDestinationsCap(t *testing.T) {
	m := New(time.Minute)
	m.SetUsers(1, []nodeapi.User{{ID: 7}, {ID: 8}})
	m.SetUsers(2, []nodeapi.User{{ID: 7}})
	m.SetDestinations(true)
	m.dest.max = 3
	open := func(node, user int64, dest string, up int) {
		s, err := m.Open(node, user, ip("1.1.1.1"), nil)
		if err != nil {
			t.Fatal(err)
		}
		s.SetDestination(Classify(dest))
		s.Up(up)
		s.Close()
	}
	open(1, 7, "a.com", 1)
	open(1, 7, "b.com", 1)
	open(1, 8, "c.com", 1)
	open(1, 7, "d.com", 10) // 超出上限
	open(1, 7, "d.com", 10) // 同一个被合并的目标只计一次
	open(1, 8, "e.com", 5)
	open(1, 7, "a.com", 1) // 已有条目照常累加
	open(2, 7, "z.com", 1) // 上限按节点计算
	r := m.CollectDestinations()
	n1 := r[1]
	if len(n1.Destinations) != 5 { // a b c + 两个用户各一个「其他」
		t.Fatalf("entries: %+v", n1.Destinations)
	}
	if n1.DestinationsMerged != 2 {
		t.Fatalf("merged: %d", n1.DestinationsMerged)
	}
	o7 := findDest(n1, 7, nodeapi.DestinationOtherName)
	if o7 == nil || o7.Kind != nodeapi.DestinationOther || o7.Up != 20 || o7.Conns != 2 || o7.Merged != 1 {
		t.Fatalf("other for user 7: %+v", o7)
	}
	if o8 := findDest(n1, 8, nodeapi.DestinationOtherName); o8 == nil || o8.Up != 5 || o8.Merged != 1 {
		t.Fatalf("other for user 8: %+v", o8)
	}
	if a := findDest(n1, 7, "a.com"); a == nil || a.Up != 2 {
		t.Fatalf("a.com: %+v", a)
	}
	if z := findDest(r[2], 7, "z.com"); z == nil {
		t.Fatalf("node 2 must have its own cap: %+v", r[2])
	}
	// 新周期重新计算上限
	for i := range 3 {
		open(1, 7, fmt.Sprintf("n%d.com", i), 1)
	}
	if n1 := m.CollectDestinations()[1]; len(n1.Destinations) != 3 || n1.DestinationsMerged != 0 {
		t.Fatalf("next period: %+v", n1)
	}
}

func TestDestinationsToggle(t *testing.T) {
	m := New(time.Minute)
	m.SetUsers(1, []nodeapi.User{{ID: 7}})
	s, _ := m.Open(1, 7, ip("1.1.1.1"), nil)
	s.SetDestination(Classify("a.com")) // 未开启：不登记
	s.Up(10)
	m.SetDestinations(true)
	if r := m.CollectDestinations(); len(r) != 0 {
		t.Fatalf("collected while disabled: %+v", r)
	}
	s2, _ := m.Open(1, 7, ip("1.1.1.1"), nil)
	s2.SetDestination(Classify("b.com"))
	s2.Up(10)
	s2.UpTo(10, "ip", "9.9.9.9")
	// 关闭：未上报的统计丢弃，之后的流量只计入用户
	m.SetDestinations(false)
	s2.Up(10)
	if r := m.CollectDestinations(); len(r) != 0 {
		t.Fatalf("collected after disable: %+v", r)
	}
	if m.DestinationsEnabled() {
		t.Fatal("should be disabled")
	}
	m.SetDestinations(true)
	s2.Up(10) // 关闭前登记的计数器已脱离统计表，不会再出现
	if r := m.CollectDestinations(); len(r) != 0 {
		t.Fatalf("stale counter reported: %+v", r)
	}
	if tr := m.Collect()[1].Traffic[7]; tr[0] != 50 {
		t.Fatalf("user traffic must keep counting: %v", tr)
	}
	s.Close()
	s2.Close()
}
