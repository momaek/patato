package meter

import (
	"net/netip"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
)

type closer struct{ closed bool }

func (c *closer) Close() error { c.closed = true; return nil }

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestTrafficAndAlive(t *testing.T) {
	m := New(time.Minute)
	m.SetUsers(1, []nodeapi.User{{ID: 7}})
	s, err := m.Open(1, 7, ip("::ffff:1.2.3.4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Up(100)
	s.Down(1000)
	r := m.Collect()[1]
	if r == nil || r.Traffic[7] != [2]int64{100, 1000} {
		t.Fatalf("traffic: %+v", r)
	}
	if got := r.Alive[7]; len(got) != 1 || got[0] != "1.2.3.4" {
		t.Fatalf("alive: %v", got)
	}
	// 清零后不再重复上报
	if r := m.Collect()[1]; r != nil && len(r.Traffic) > 0 {
		t.Fatalf("traffic reported twice: %+v", r.Traffic)
	}
	s.Close()
}

func TestUnknownUserAndRemoval(t *testing.T) {
	m := New(time.Minute)
	if _, err := m.Open(1, 7, ip("1.1.1.1"), nil); err != ErrNodeNotServed {
		t.Fatalf("want ErrNodeNotServed, got %v", err)
	}
	m.SetUsers(1, []nodeapi.User{{ID: 7}})
	if _, err := m.Open(1, 8, ip("1.1.1.1"), nil); err != ErrUnknownUser {
		t.Fatalf("want ErrUnknownUser, got %v", err)
	}
	c := &closer{}
	if _, err := m.Open(1, 7, ip("1.1.1.1"), c); err != nil {
		t.Fatal(err)
	}
	m.SetUsers(1, nil)
	if !c.closed {
		t.Fatal("connection of removed user must be closed")
	}
	if m.Connections(1) != 0 {
		t.Fatal("session not released")
	}
}

func TestDeviceLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := New(time.Minute)
	m.now = func() time.Time { return now }
	m.SetUsers(1, []nodeapi.User{{ID: 7, DeviceLimit: 2}})
	a, err := m.Open(1, 7, ip("1.1.1.1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Open(1, 7, ip("1.1.1.1"), nil)
	if err != nil {
		t.Fatal("same IP is the same device")
	}
	if _, err := m.Open(1, 7, ip("2.2.2.2"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(1, 7, ip("3.3.3.3"), nil); err != ErrDeviceLimit {
		t.Fatalf("third device: %v", err)
	}
	// 其他机器上的设备也计入
	m.SetRemote(map[int64]int{7: 1})
	a.Close()
	b.Close()
	m.Collect()
	now = now.Add(2 * time.Minute)
	m.Collect()
	if _, err := m.Open(1, 7, ip("3.3.3.3"), nil); err != ErrDeviceLimit {
		t.Fatalf("remote device should count: %v", err)
	}
	m.SetRemote(nil)
	if _, err := m.Open(1, 7, ip("3.3.3.3"), nil); err != nil {
		t.Fatalf("expired device should free a slot: %v", err)
	}
}

func TestSpeedLimit(t *testing.T) {
	m := New(time.Minute)
	m.SetUsers(1, []nodeapi.User{{ID: 7, SpeedLimit: 1}}) // 1 Mbps = 125000 B/s
	s, err := m.Open(1, 7, ip("1.1.1.1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Limited() {
		t.Fatal("expected limiter")
	}
	start := time.Now()
	s.Down(125000) // 首个突发
	s.Down(62500)  // 需要再等约 0.5 秒
	if d := time.Since(start); d < 400*time.Millisecond || d > 2*time.Second {
		t.Fatalf("rate limit took %v", d)
	}
}
