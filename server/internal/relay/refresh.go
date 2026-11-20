package relay

import (
	"context"
	"net"

	"github.com/momaek/patato/server/internal/store/db"
)

// RefreshForwardTargets 重新计算一台机器相关的转发的外网端口和下一跳：这台机器上的转发（外网端口），
// 以及以这台机器为下一跳的转发（目标地址和端口）。返回更新的条数。地址变化由触发器通知 patato-node。
func RefreshForwardTargets(ctx context.Context, q *db.Queries, machineID int64) (int, error) {
	w, err := Load(ctx, q)
	if err != nil {
		return 0, err
	}
	entries, err := q.ListEntries(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		fs := w.Forwards[e.ID]
		if e.RouteID == nil || len(fs) == 0 {
			continue
		}
		touches := false
		for _, f := range fs {
			if f.MachineID == machineID {
				touches = true
			}
		}
		node, err := q.GetNode(ctx, e.NodeID)
		if err != nil {
			return n, err
		}
		landing, hasLanding := w.MachinesByHost[node.Host]
		if !touches && !(hasLanding && landing.ID == machineID) {
			continue
		}
		c, err := refreshEntry(ctx, q, w, node, fs)
		if err != nil {
			return n, err
		}
		n += c
	}
	return n, nil
}

// refreshEntry 按当前机器属性重算一个入口各跳的外网端口和下一跳，只更新有变化的转发。
func refreshEntry(ctx context.Context, q *db.Queries, w *World, node db.Node, fs []db.Forward) (int, error) {
	hops := make([]db.Machine, len(fs))
	ports := make([]HopPorts, len(fs))
	for i, f := range fs {
		hops[i] = w.Machines[f.MachineID]
		ports[i] = HopPorts{MachineID: f.MachineID, Listen: int(f.ListenPort), Public: PublicPort(hops[i], int(f.ListenPort))}
	}
	var landing *db.Machine
	if m, ok := w.MachinesByHost[node.Host]; ok {
		landing = &m
	}
	targets := Targets(node, landing, hops, ports)
	n := 0
	for i, f := range fs {
		if f.Mode == "manual" {
			continue
		}
		t := targets[i]
		if int(f.PublicPort) == ports[i].Public && f.TargetHost == t.Host && int(f.TargetPort) == t.Port {
			continue
		}
		ip := f.TargetIp
		if f.TargetHost != t.Host {
			ip = LiteralIP(t.Host)
		}
		if err := q.SetForwardEndpoints(ctx, db.SetForwardEndpointsParams{
			ID: f.ID, PublicPort: int32(ports[i].Public), TargetHost: t.Host, TargetPort: int32(t.Port), TargetIp: ip,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// LiteralIP 在 host 本身是 IP 时返回它，域名返回空串（由解析任务填写）。
func LiteralIP(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// RefreshNodeForwards 在节点的对外端口或地址变化后，重算它所有中转入口最后一跳的目标。
func RefreshNodeForwards(ctx context.Context, q *db.Queries, nodeID int64) (int, error) {
	entries, err := q.ListNodeEntries(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	node, err := q.GetNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	var w *World
	n := 0
	for _, e := range entries {
		if e.RouteID == nil {
			continue
		}
		if w == nil {
			if w, err = Load(ctx, q); err != nil {
				return 0, err
			}
		}
		fs := w.Forwards[e.ID]
		if len(fs) == 0 {
			continue
		}
		c, err := refreshEntry(ctx, q, w, node, fs)
		if err != nil {
			return n, err
		}
		n += c
	}
	return n, nil
}
