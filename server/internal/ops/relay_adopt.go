package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- entry.adopt：链路发现确认后，把入口登记为经过某条线路的手写转发 ---------

// EntryAdopt 把一个自定义入口（例如 Xboard 导入的子节点）登记为经过线路 route 的入口，转发为手写（manual）：
// 只记录每跳端口和下一跳，不下发、不计流量。入口的地址和端口保持不变，订阅内容不变，此后能按跳监控在线状态。
type EntryAdopt struct{ d *Deps }

func (*EntryAdopt) Kind() string      { return "entry.adopt" }
func (*EntryAdopt) Risk() change.Risk { return change.Normal }

type entryAdoptParams struct {
	Node     Ref             `json:"node"`
	Entry    Ref             `json:"entry"`
	Route    Ref             `json:"route"`
	Forwards []manualForward `json:"forwards"`
}

type entryAdoptResolved struct {
	EntryID  int64            `json:"entry_id"`
	RouteID  int64            `json:"route_id"`
	Forwards []plannedForward `json:"forwards"`
}

func (o *EntryAdopt) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[entryAdoptParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(ctx, q, n, p.Entry)
	if err != nil {
		return nil, err
	}
	if relay.EntryKind(e) != relay.KindCustom {
		return nil, apierr.Conflict("entry_not_custom", "入口 %d 不是只填了地址的入口，不能登记线路", e.ID).
			WithHint("直连入口不经过中转；已经绑定线路的入口先删除再重新添加")
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	ms, err := q.ListMachinesByIDs(ctx, r.Hops)
	if err != nil {
		return nil, err
	}
	pe, err := planManualEntry(ctx, q, n, r, orderMachines(ms, r.Hops), p.Forwards)
	if err != nil {
		return nil, err
	}
	name := relay.EntryName(n, e, map[int64]db.Route{r.ID: r})
	return &change.Draft{
		Summary: fmt.Sprintf("登记入口「%s」经过线路 %s", name, r.Name),
		Steps: []string{
			fmt.Sprintf("登记手写转发（不下发、不计流量）：%s", describeForwards(pe.Forwards)),
			"入口地址和端口不变，订阅内容不变；此后任一跳机器离线时入口从订阅中撤下并告警",
		},
		Diff:     []change.DiffEntry{{Field: "route", Before: nil, After: r.Code}},
		Targets:  []change.Target{entryTarget(e), routeTarget(r)},
		Resolved: entryAdoptResolved{EntryID: e.ID, RouteID: r.ID, Forwards: pe.Forwards},
	}, nil
}

func (o *EntryAdopt) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[entryAdoptResolved](raw)
	if err != nil {
		return nil, err
	}
	if err := q.SetEntryRoute(ctx, db.SetEntryRouteParams{ID: r.EntryID, RouteID: &r.RouteID}); err != nil {
		return nil, err
	}
	for _, f := range r.Forwards {
		_, err := q.CreateForward(ctx, db.CreateForwardParams{
			EntryID: r.EntryID, Hop: int32(f.Hop), MachineID: f.MachineID, ListenPort: int32(f.ListenPort), PublicPort: int32(f.PublicPort),
			TargetHost: f.TargetHost, TargetPort: int32(f.TargetPort), Mode: "manual", TargetIp: relay.LiteralIP(f.TargetHost),
		})
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("port_taken", "机器 %s 的端口 %d 已登记过转发", f.Machine, f.ListenPort)
		}
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"entry_id": r.EntryID, "route_id": r.RouteID}, nil
}
