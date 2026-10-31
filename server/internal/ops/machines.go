package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- machine.revoke ----------------------------------------------------

// MachineRevoke 吊销机器 token：这台机器上的 patato-node 立即无法同步和上报，
// 需要重新用 patato machine install-cmd 接入。用于机器泄露或下线。
type MachineRevoke struct{ d *Deps }

func (*MachineRevoke) Kind() string      { return "machine.revoke" }
func (*MachineRevoke) Risk() change.Risk { return change.Normal }

type machineRefParams struct {
	Machine string `json:"machine"`
}

func findMachine(ctx context.Context, q *db.Queries, host string) (db.Machine, error) {
	if host == "" {
		return db.Machine{}, apierr.BadRequest("missing_machine", "缺少机器").WithHint("传机器名（与节点的 host 一致）；用 GET /api/v1/machines 查看全部")
	}
	m, err := q.GetMachineByHost(ctx, host)
	if store.IsNotFound(err) {
		return m, apierr.NotFound("machine_not_found", "机器 %s 没有接入过 patato-node", host).WithHint("用 GET /api/v1/machines 查看全部")
	}
	return m, err
}

func (o *MachineRevoke) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[machineRefParams](raw)
	if err != nil {
		return nil, err
	}
	m, err := findMachine(ctx, q, p.Machine)
	if err != nil {
		return nil, err
	}
	if m.TokenHash == nil && m.EnrollHash == nil {
		return nil, apierr.Conflict("not_enrolled", "机器 %s 当前没有有效的 token 或注册码", m.Host)
	}
	nodes, err := q.ListNativeNodesOfHost(ctx, m.Host)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersOfHost(ctx, m.Host)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("吊销机器 %s 的 patato-node 凭证", m.Host),
		Steps: []string{
			"机器 token 和未使用的注册码立即失效",
			fmt.Sprintf("机器上 %d 个节点停止同步用户和上报流量，心跳超时后转为离线，%d 名用户的订阅受影响", len(nodes), users),
			fmt.Sprintf("恢复时用 patato machine install-cmd %s 重新接入", m.Host),
		},
		Diff:    []change.DiffEntry{{Field: "机器凭证", Before: "有效", After: "已吊销"}},
		Impact:  change.Impact{Users: users, Nodes: len(nodes)},
		Targets: []change.Target{{Type: "machine", ID: m.ID, Version: m.Version}},
	}, nil
}

func (o *MachineRevoke) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[machineRefParams](raw)
	if err != nil {
		return nil, err
	}
	m, err := findMachine(ctx, q, p.Machine)
	if err != nil {
		return nil, err
	}
	if err := q.RevokeMachine(ctx, m.ID); err != nil {
		return nil, err
	}
	return map[string]any{"machine": m.Host, "revoked": true}, nil
}
