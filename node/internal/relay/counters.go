package relay

import (
	"bufio"
	"os"
	"slices"
	"strconv"
	"strings"
)

// parseCounter 解析计数器名 f<id>_up / f<id>_down。
func parseCounter(name string) (int64, string, bool) {
	if !strings.HasPrefix(name, "f") {
		return 0, "", false
	}
	idStr, dir, ok := strings.Cut(name[1:], "_")
	if !ok || (dir != "up" && dir != "down") {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, dir, true
}

// ListeningPorts 读取本机正在监听的 TCP 端口和已绑定的 UDP 端口（/proc/net/{tcp,tcp6,udp,udp6}），
// 上报给面板，分配转发端口时避开 Patato 以外的进程（例如 socat）。不是 Linux 时返回 nil。
func ListeningPorts() []int {
	seen := map[int]bool{}
	for _, f := range []struct {
		path   string
		listen bool
	}{{"/proc/net/tcp", true}, {"/proc/net/tcp6", true}, {"/proc/net/udp", false}, {"/proc/net/udp6", false}} {
		for _, p := range parseProcNet(f.path, f.listen) {
			seen[p] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// parseProcNet 解析 /proc/net/tcp 格式：local_address 列为 HEXIP:HEXPORT，st 列 0A 为 LISTEN。
// UDP 没有监听状态，取所有本地端口非 0 的套接字（st 07）。
func parseProcNet(path string, listenOnly bool) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseProcNetLines(bufio.NewScanner(f), listenOnly)
}

func parseProcNetLines(sc *bufio.Scanner, listenOnly bool) []int {
	var out []int
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		if listenOnly && fields[3] != "0A" {
			continue
		}
		_, hexPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if err != nil || p == 0 {
			continue
		}
		out = append(out, int(p))
	}
	return out
}
