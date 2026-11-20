package relay

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/momaek/patato/nodeapi"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestUserspaceForward 用真实的 sing-box direct 入站转发 TCP 与 UDP，并核对计数。
func TestUserspaceForward(t *testing.T) {
	tcpEcho, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpEcho.Close()
	go func() {
		for {
			c, err := tcpEcho.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	target := tcpEcho.Addr().(*net.TCPAddr).Port
	udpEcho, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(target))
	if err != nil {
		t.Skipf("udp port %d busy: %v", target, err)
	}
	defer udpEcho.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := udpEcho.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpEcho.WriteTo(buf[:n], addr)
		}
	}()

	u := NewUserspace("error")
	defer u.Close()
	listen := freePort(t)
	res := u.Apply([]nodeapi.Forward{{ID: 21, ListenPort: listen, TargetIP: "127.0.0.1", TargetPort: target, Mode: "userspace"}})
	if res[21] != nil {
		t.Fatalf("apply: %v", res[21])
	}

	payload := bytes.Repeat([]byte("patato"), 10000) // 60000 字节
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(listen), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.Write(payload) }()
	got := make([]byte, len(payload))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("tcp echo through relay: %v", err)
	}
	c.Close()

	uc, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(listen))
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	_ = uc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := uc.Write([]byte("hello-udp")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := uc.Read(buf)
	if err != nil || string(buf[:n]) != "hello-udp" {
		t.Fatalf("udp echo through relay: %q %v", buf[:n], err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var cnt Counters
	for time.Now().Before(deadline) {
		cs, _ := u.Read()
		cnt = cs[21]
		if cnt.Up >= uint64(len(payload)+9) && cnt.Down >= uint64(len(payload)+9) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cnt.Up < uint64(len(payload)+9) || cnt.Down < uint64(len(payload)+9) {
		t.Fatalf("counters %+v, want at least %d each way", cnt, len(payload)+9)
	}

	// 删除后端口不再监听
	u.Apply(nil)
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(listen), 500*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("port should be closed after the forward is removed")
	}
}
