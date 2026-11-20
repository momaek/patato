package native

// 落地访问统计的入账：patato-node 每 5 分钟随上报带上「用户 × 目标」聚合，与流量在同一个事务里按序号去重，
// 按小时累加进 destination_stats_hourly。只接受这台机器上的节点；面板关闭统计后到达的批次（节点尚未同步到关闭）丢弃。

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/store/db"
)

// maxDestLen 是目标名称的最大长度（域名最长 253）。
const maxDestLen = 255

func (h *Handler) storeDestinations(ctx context.Context, q *db.Queries, byID map[int64]db.Node, r nodeapi.NodeReport, at time.Time) error {
	if !h.cfg.Destinations {
		return nil
	}
	if _, ok := byID[r.ID]; !ok {
		return nil
	}
	if at.IsZero() || at.After(time.Now().Add(time.Hour)) {
		at = time.Now()
	}
	type key struct {
		user       int64
		kind, dest string
	}
	agg := map[key]*nodeapi.DestinationStat{}
	order := []key{}
	limit := nodeapi.MaxDestinations * 2 // 正常不会超过上限加每个用户一个「其他」；防御异常上报
	for _, d := range r.Destinations {
		if d.User <= 0 || d.Up < 0 || d.Down < 0 || d.Conns < 0 || d.Merged < 0 {
			continue
		}
		switch d.Kind {
		case nodeapi.DestinationDomain, nodeapi.DestinationIP:
		case nodeapi.DestinationOther:
			d.Dest = nodeapi.DestinationOtherName
		default:
			continue
		}
		if d.Dest == "" || len(d.Dest) > maxDestLen || !utf8.ValidString(d.Dest) {
			continue
		}
		k := key{d.User, d.Kind, d.Dest}
		if a := agg[k]; a != nil {
			a.Up, a.Down, a.Conns, a.Merged = a.Up+d.Up, a.Down+d.Down, a.Conns+d.Conns, a.Merged+d.Merged
			continue
		}
		if len(order) >= limit {
			continue
		}
		dd := d
		agg[k] = &dd
		order = append(order, k)
	}
	if len(order) == 0 {
		return nil
	}
	p := db.AddDestinationStatsParams{NodeID: r.ID, Hour: at.UTC().Truncate(time.Hour)}
	for _, k := range order {
		d := agg[k]
		p.UserIds = append(p.UserIds, d.User)
		p.Kinds = append(p.Kinds, d.Kind)
		p.Dests = append(p.Dests, d.Dest)
		p.Ups = append(p.Ups, d.Up)
		p.Downs = append(p.Downs, d.Down)
		p.Conns = append(p.Conns, d.Conns)
		p.Merged = append(p.Merged, d.Merged)
	}
	return q.AddDestinationStats(ctx, p)
}
