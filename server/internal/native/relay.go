package native

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store/db"
)

// 中转转发：sync 下发本机应有的完整转发列表，report 接收流量差值、应用结果与探测结果。

// ProbeInterval 是 patato-node 对每条转发的下一跳做 TCP 探测的间隔（秒）。
const ProbeInterval = 60

// Resolver 把下一跳的域名解析成 IP；测试里可以替换。
type Resolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// SetResolver 替换 DNS 解析器（默认使用系统解析）。
func (h *Handler) SetResolver(r Resolver) { h.resolver = r }

// forwardState 计算一台机器应有的转发列表。下一跳是域名且还没解析过的，在这里解析一次并保存；
// 解析失败的转发不下发，并标记为失败，入口因此不会出现在订阅里。
func (h *Handler) forwardState(ctx context.Context, m db.Machine) (*nodeapi.ForwardSync, error) {
	if m.ID == 0 {
		return nil, nil
	}
	rows, err := h.st.ListManagedMachineForwards(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	items := make([]nodeapi.Forward, 0, len(rows))
	for _, f := range rows {
		ip := f.TargetIp
		if ip == "" {
			ip, err = h.resolveTarget(ctx, f)
			if err != nil {
				_ = h.st.SetForwardReport(ctx, db.SetForwardReportParams{ID: f.ID, Status: "failed", AppliedMode: f.AppliedMode,
					Error: fmt.Sprintf("解析下一跳 %s 失败：%v", f.TargetHost, err)})
				continue
			}
		}
		mode := f.Mode
		if m.ForwardMode == "userspace" {
			mode = "userspace"
		}
		items = append(items, nodeapi.Forward{ID: f.ID, ListenPort: int(f.ListenPort), TargetIP: ip, TargetPort: int(f.TargetPort), Mode: mode})
	}
	slices.SortFunc(items, func(a, b nodeapi.Forward) int { return int(a.ID - b.ID) })
	return &nodeapi.ForwardSync{Rev: nodeapi.Rev(items), Items: items}, nil
}

// resolveTarget 解析下一跳并保存结果，优先 IPv4（nft 规则只做 IPv4 DNAT）。
func (h *Handler) resolveTarget(ctx context.Context, f db.Forward) (string, error) {
	ip, err := ResolveHost(ctx, h.resolver, f.TargetHost)
	if err != nil {
		return "", err
	}
	if err := h.st.SetForwardTargetIP(ctx, db.SetForwardTargetIPParams{ID: f.ID, TargetIp: ip}); err != nil {
		return "", err
	}
	return ip, nil
}

// ResolveHost 解析下一跳地址：本身是 IP 时原样返回；域名取第一个 IPv4，没有 IPv4 时取第一个地址。
func ResolveHost(ctx context.Context, r Resolver, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("没有解析到地址")
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return ips[0].String(), nil
}

// RefreshTargets 重新解析所有下一跳是域名的转发；结果变化时更新 target_ip，数据库触发器通知 patato-node 重新同步。
// 返回变化的条数，由周期任务调用。
func (h *Handler) RefreshTargets(ctx context.Context) (int, error) {
	rows, err := h.st.ListForwards(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, f := range rows {
		if f.Mode == "manual" || net.ParseIP(f.TargetHost) != nil {
			continue
		}
		ip, err := ResolveHost(ctx, h.resolver, f.TargetHost)
		if err != nil {
			h.log.Warn("resolve forward target", "forward", f.ID, "host", f.TargetHost, "err", err)
			continue
		}
		if ip == f.TargetIp {
			continue
		}
		if err := h.st.SetForwardTargetIP(ctx, db.SetForwardTargetIPParams{ID: f.ID, TargetIp: ip}); err != nil {
			return changed, err
		}
		h.log.Info("forward target address changed", "forward", f.ID, "host", f.TargetHost, "from", f.TargetIp, "to", ip)
		changed++
	}
	return changed, nil
}

// RefreshEntryAddresses 解析有入口组的线路的入口地址，记下它当前指向的 IP（全部 A / AAAA 记录），
// 用来判断域名指向入口组里的哪台机器。解析失败时不更新；结果超过 relay.EntryIPsTTL 没刷新就不再采信。返回结果变化的线路数，由周期任务调用。
func (h *Handler) RefreshEntryAddresses(ctx context.Context) (int, error) {
	routes, err := h.st.ListRoutes(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, r := range routes {
		if len(r.EntryPeers) == 0 || r.EntryAddress == "" {
			continue
		}
		ips, err := lookupAll(ctx, h.resolver, r.EntryAddress)
		if err != nil {
			h.log.Warn("resolve route entry address", "route", r.Code, "host", r.EntryAddress, "err", err)
			continue
		}
		if err := h.st.SetRouteEntryIPs(ctx, db.SetRouteEntryIPsParams{ID: r.ID, EntryIps: ips, EntryAddress: r.EntryAddress}); err != nil {
			return changed, err
		}
		if !slices.Equal(ips, r.EntryIps) {
			h.log.Info("route entry address now points elsewhere", "route", r.Code, "host", r.EntryAddress, "from", r.EntryIps, "to", ips)
			changed++
		}
	}
	return changed, nil
}

// lookupAll 返回地址的全部 IP，排序去重；本身是 IP 时原样返回。
func lookupAll(ctx context.Context, r Resolver, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if s := ip.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有解析到地址")
	}
	slices.Sort(out)
	return out, nil
}

// storeForwards 在上报事务里处理转发：流量差值进小时表，应用结果与探测结果写回转发。返回需要在事务外发出的事件。
func storeForwards(ctx context.Context, q *db.Queries, m db.Machine, req nodeapi.ReportRequest) ([]events.Event, []string, error) {
	if len(req.Forwards) == 0 {
		return nil, nil, nil
	}
	rows, err := q.ListMachineForwards(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]db.Forward, len(rows))
	for _, f := range rows {
		byID[f.ID] = f
	}
	routeOf := map[int64]*int64{}
	at := req.At
	if at.IsZero() || at.After(time.Now().Add(5*time.Minute)) {
		at = time.Now()
	}
	hour := at.UTC().Truncate(time.Hour)
	var evs []events.Event
	var resolve []string
	for _, r := range req.Forwards {
		f, ok := byID[r.ID]
		if !ok {
			// 转发刚被删除：流量照样按入口已不存在丢弃，只有还在这台机器上的转发才入账
			continue
		}
		if r.Up > 0 || r.Down > 0 {
			rid, ok := routeOf[f.EntryID]
			if !ok {
				if e, err := q.GetEntry(ctx, f.EntryID); err == nil {
					rid = e.RouteID
				}
				routeOf[f.EntryID] = rid
			}
			if err := q.AddForwardTrafficHourly(ctx, db.AddForwardTrafficHourlyParams{
				ForwardID: f.ID, Hour: hour, RouteID: rid, MachineID: m.ID, Up: max(r.Up, 0), Down: max(r.Down, 0),
			}); err != nil {
				return nil, nil, err
			}
		}
		status := "failed"
		if r.Applied {
			status = "active"
		}
		var appliedAt *time.Time
		if r.Applied && !r.At.IsZero() {
			t := r.At
			appliedAt = &t
		}
		if err := q.SetForwardReport(ctx, db.SetForwardReportParams{ID: f.ID, Status: status, AppliedMode: r.Mode, Error: truncate(r.Error, 500), AppliedAt: appliedAt}); err != nil {
			return nil, nil, err
		}
		key := fmt.Sprintf("%s:%d", events.ForwardApplyFailed, f.ID)
		switch {
		case !r.Applied && f.Status != "failed":
			evs = append(evs, events.Event{
				Kind: events.ForwardApplyFailed, Severity: "critical", Subject: fmt.Sprintf("forward:%d", f.ID),
				Message:  fmt.Sprintf("机器 %s 上端口 %d 的转发应用失败：%s", m.Host, f.ListenPort, r.Error),
				Payload:  map[string]any{"forward_id": f.ID, "entry_id": f.EntryID, "machine": m.Host, "port": f.ListenPort, "error": r.Error},
				DedupKey: key,
			})
		case r.Applied && f.Status == "failed":
			resolve = append(resolve, key)
		}
		if p := r.Probe; p != nil {
			ok := p.Last
			var ms *int32
			if p.AvgMS > 0 {
				v := int32(p.AvgMS)
				ms = &v
			}
			pat := p.At
			if pat.IsZero() {
				pat = at
			}
			prev, err := q.SetForwardProbe(ctx, db.SetForwardProbeParams{ID: f.ID, Ok: &ok, Ms: ms, At: &pat, Failures: int32(p.Consecutive)})
			if err != nil {
				return nil, nil, err
			}
			if err := q.AddForwardProbe(ctx, db.AddForwardProbeParams{ForwardID: f.ID, Hour: hour, Ok: int32(p.OK), Fail: int32(p.Fail),
				LatencyMs: int64(p.AvgMS) * int64(p.OK)}); err != nil {
				return nil, nil, err
			}
			pkey := fmt.Sprintf("%s:%d", events.RouteProbeFailed, f.ID)
			const limit = 3
			switch {
			case p.Consecutive >= limit && prev < limit:
				evs = append(evs, events.Event{
					Kind: events.RouteProbeFailed, Severity: "critical", Subject: fmt.Sprintf("forward:%d", f.ID),
					Message: fmt.Sprintf("机器 %s 到下一跳 %s:%d 连续 %d 次 TCP 探测失败，入口已从订阅中撤下",
						m.Host, f.TargetHost, f.TargetPort, p.Consecutive),
					Payload:  map[string]any{"forward_id": f.ID, "entry_id": f.EntryID, "machine": m.Host, "target": fmt.Sprintf("%s:%d", f.TargetHost, f.TargetPort)},
					DedupKey: pkey,
				})
			case p.Consecutive == 0 && prev >= limit:
				resolve = append(resolve, pkey)
			}
		}
	}
	return evs, resolve, nil
}

// listeningPorts 去重、排序本机正在监听的端口。
func listeningPorts(ports []int) []int32 {
	out := make([]int32, 0, len(ports))
	seen := map[int]bool{}
	for _, p := range ports {
		if p > 0 && p <= 65535 && !seen[p] {
			seen[p] = true
			out = append(out, int32(p))
		}
	}
	slices.Sort(out)
	return out
}
