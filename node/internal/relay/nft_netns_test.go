//go:build linux && netns

// 需要 root 的网络命名空间测试：go test -tags netns,with_quic,with_utls,with_acme ./node/internal/relay/ （以 root 运行）。
// 拓扑：client(10.10.1.2) — relay(10.10.1.1 | 10.10.2.1) — server(10.10.2.2)。
// relay 命名空间里应用 nft 规则集：relay:41010 → server:8080，验证 TCP / UDP 连通、forward 链按字节计数、
// 只动 table inet patato、整表替换后旧规则消失。用 ip 命令搭拓扑，CI 中以特权容器运行。

package relay

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
	"golang.org/x/sys/unix"

	"github.com/momaek/patato/nodeapi"
)

func sh(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func inNS(t *testing.T, ns string, f func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	orig, err := os.Open("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	defer orig.Close()
	target, err := os.Open("/var/run/netns/" + ns)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		t.Fatal(err)
	}
	defer unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET)
	f()
}

func TestNFTNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	suffix := fmt.Sprintf("%d", os.Getpid())
	cl, rl, sv := "pc"+suffix, "pr"+suffix, "ps"+suffix
	for _, ns := range []string{cl, rl, sv} {
		sh(t, "ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
	}
	sh(t, "ip", "link", "add", "vc"+suffix, "netns", cl, "type", "veth", "peer", "name", "vr1"+suffix, "netns", rl)
	sh(t, "ip", "link", "add", "vs"+suffix, "netns", sv, "type", "veth", "peer", "name", "vr2"+suffix, "netns", rl)
	sh(t, "ip", "-n", cl, "addr", "add", "10.10.1.2/24", "dev", "vc"+suffix)
	sh(t, "ip", "-n", rl, "addr", "add", "10.10.1.1/24", "dev", "vr1"+suffix)
	sh(t, "ip", "-n", rl, "addr", "add", "10.10.2.1/24", "dev", "vr2"+suffix)
	sh(t, "ip", "-n", sv, "addr", "add", "10.10.2.2/24", "dev", "vs"+suffix)
	for _, x := range [][2]string{{cl, "vc"}, {rl, "vr1"}, {rl, "vr2"}, {sv, "vs"}} {
		sh(t, "ip", "-n", x[0], "link", "set", x[1]+suffix, "up")
		sh(t, "ip", "-n", x[0], "link", "set", "lo", "up")
	}
	sh(t, "ip", "netns", "exec", rl, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	// server 没有回 client 的路由：只有 masquerade 生效时回包才能回来

	// server：TCP / UDP 回显
	var tcpL net.Listener
	var udpL net.PacketConn
	inNS(t, sv, func() {
		var err error
		if tcpL, err = net.Listen("tcp", "10.10.2.2:8080"); err != nil {
			t.Fatal(err)
		}
		if udpL, err = net.ListenPacket("udp", "10.10.2.2:8080"); err != nil {
			t.Fatal(err)
		}
	})
	defer tcpL.Close()
	defer udpL.Close()
	go func() {
		for {
			c, err := tcpL.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, err := udpL.ReadFrom(buf)
			if err != nil {
				return
			}
			udpL.WriteTo(buf[:n], a)
		}
	}()

	nsFile, err := os.Open("/var/run/netns/" + rl)
	if err != nil {
		t.Fatal(err)
	}
	defer nsFile.Close()
	n, err := newNFT(int(nsFile.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	// 别的表不受影响
	c, _ := nftables.New(nftables.WithNetNSFd(int(nsFile.Fd())))
	other := c.AddTable(&nftables.Table{Name: "other", Family: nftables.TableFamilyINet})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	res := n.Apply([]nodeapi.Forward{{ID: 12, ListenPort: 41010, TargetIP: "10.10.2.2", TargetPort: 8080}})
	if res[12] != nil {
		t.Fatalf("apply: %v", res[12])
	}

	payload := bytes.Repeat([]byte("x"), 100000)
	inNS(t, cl, func() {
		conn, err := net.DialTimeout("tcp", "10.10.1.1:41010", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		go conn.Write(payload)
		got := make([]byte, len(payload))
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		uc, err := net.Dial("udp", "10.10.1.1:41010")
		if err != nil {
			t.Fatal(err)
		}
		uc.SetDeadline(time.Now().Add(3 * time.Second))
		uc.Write([]byte("ping"))
		buf := make([]byte, 16)
		if k, err := uc.Read(buf); err != nil || string(buf[:k]) != "ping" {
			t.Fatalf("udp: %q %v", buf[:k], err)
		}
		uc.Close()
	})
	cs, err := n.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cs[12].Up < uint64(len(payload)) || cs[12].Down < uint64(len(payload)) {
		t.Fatalf("forward chain counters %+v, want >= %d each way", cs[12], len(payload))
	}

	// 整表替换：空列表删掉 patato 表，other 表还在
	n.Apply(nil)
	ts, _ := c.ListTablesOfFamily(nftables.TableFamilyINet)
	names := []string{}
	for _, tb := range ts {
		names = append(names, tb.Name)
	}
	if strings.Contains(strings.Join(names, ","), TableName) || !strings.Contains(strings.Join(names, ","), other.Name) {
		t.Fatalf("tables after empty apply: %v", names)
	}
}
