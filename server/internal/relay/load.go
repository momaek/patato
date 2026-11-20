package relay

import (
	"context"

	"github.com/momaek/patato/server/internal/store/db"
)

// Load 读取计算入口需要的线路、机器和转发。数据量与线路、入口数成正比（几十到几百行），每次整表读取。
func Load(ctx context.Context, q *db.Queries) (*World, error) {
	routes, err := q.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	machines, err := q.ListMachines(ctx)
	if err != nil {
		return nil, err
	}
	forwards, err := q.ListForwards(ctx)
	if err != nil {
		return nil, err
	}
	return NewWorld(routes, machines, forwards), nil
}

// Endpoints 返回一个节点所有入口的订阅地址与可用性；没有入口的节点按一个启用的直连入口处理。
// 停用的入口不返回。
func (w *World) Endpoints(n db.Node, entries []db.NodeEntry) []Endpoint {
	if len(entries) == 0 {
		return []Endpoint{w.Resolve(n, Implicit(n))}
	}
	out := make([]Endpoint, 0, len(entries))
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		out = append(out, w.Resolve(n, e))
	}
	return out
}

// GroupEntries 按节点分组。
func GroupEntries(entries []db.NodeEntry) map[int64][]db.NodeEntry {
	out := map[int64][]db.NodeEntry{}
	for _, e := range entries {
		out[e.NodeID] = append(out[e.NodeID], e)
	}
	return out
}
