-- 中转线路：线路、入口、转发与线路流量 ------------------------------------

-- name: GetMachine :one
SELECT * FROM machines WHERE id = $1;

-- name: ListMachinesByIDs :many
SELECT * FROM machines WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SetMachineNetwork :one
UPDATE machines SET public_address = $2, port_ranges = $3, nat = $4, forward_mode = $5, reserved_ports = $6,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: SetMachineListening :exec
UPDATE machines SET listening_ports = $2 WHERE id = $1;

-- name: SetMachineRelayRuntime :exec
UPDATE machines SET relay_runtime = $2 WHERE id = $1;

-- name: SetMachineTolato :exec
-- tolato 上报的 IP 和在线状态，由周期任务刷新。
UPDATE machines SET tolato_ip = $2, tolato_online = $3 WHERE id = $1;

-- name: SetMachineState :one
-- 只在状态变化时更新，返回变化前的状态；没有变化时不返回行。
UPDATE machines SET state = sqlc.arg(state), state_changed_at = now()
FROM (SELECT o.id, o.state FROM machines o WHERE o.id = sqlc.arg(id)::bigint FOR UPDATE) AS prev
WHERE machines.id = prev.id AND prev.state <> sqlc.arg(state)
RETURNING prev.state AS previous_state;

-- name: ListRelayMachines :many
-- 出现在任意线路里的机器。
SELECT * FROM machines WHERE id IN (SELECT unnest(hops) FROM routes) ORDER BY id;

-- 线路 -------------------------------------------------------------------

-- name: ListRoutes :many
SELECT * FROM routes ORDER BY code;

-- name: GetRoute :one
SELECT * FROM routes WHERE id = $1;

-- name: GetRouteByCode :one
SELECT * FROM routes WHERE code = $1;

-- name: CreateRoute :one
INSERT INTO routes (code, name, hops) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateRoute :one
UPDATE routes SET name = $2, hops = $3, updated_at = now(), version = version + 1 WHERE id = $1 RETURNING *;

-- name: DeleteRoute :exec
DELETE FROM routes WHERE id = $1;

-- name: RouteVersionForUpdate :one
SELECT version FROM routes WHERE id = $1 FOR UPDATE;

-- name: ListRoutesWithMachine :many
SELECT * FROM routes WHERE sqlc.arg(machine_id)::bigint = ANY(hops) ORDER BY id;

-- 入口 -------------------------------------------------------------------

-- name: ListEntries :many
SELECT * FROM node_entries ORDER BY node_id, sort, id;

-- name: ListNodeEntries :many
SELECT * FROM node_entries WHERE node_id = $1 ORDER BY sort, id;

-- name: ListEntriesOfNodes :many
SELECT * FROM node_entries WHERE node_id = ANY(sqlc.arg(node_ids)::bigint[]) ORDER BY node_id, sort, id;

-- name: ListRouteEntries :many
SELECT * FROM node_entries WHERE route_id = $1 ORDER BY node_id, sort, id;

-- name: GetEntry :one
SELECT * FROM node_entries WHERE id = $1;

-- name: CreateEntry :one
INSERT INTO node_entries (node_id, route_id, suffix, name, address, port, enabled, sort)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: SetEntryEnabled :exec
UPDATE node_entries SET enabled = $2, updated_at = now(), version = version + 1 WHERE id = $1;

-- name: DeleteEntry :exec
DELETE FROM node_entries WHERE id = $1;

-- name: EntryVersionForUpdate :one
SELECT version FROM node_entries WHERE id = $1 FOR UPDATE;

-- name: CountUsersOfEntries :one
-- 入口所在节点挂的套餐里的用户数，用于计算风险等级。
SELECT count(DISTINCT u.id) FROM users u
JOIN plan_nodes pn ON pn.plan_id = u.plan_id
JOIN node_entries e ON e.node_id = pn.node_id
WHERE e.id = ANY(sqlc.arg(entry_ids)::bigint[]);

-- name: CountUsersOfNodes :one
SELECT count(DISTINCT u.id) FROM users u
JOIN plan_nodes pn ON pn.plan_id = u.plan_id
WHERE pn.node_id = ANY(sqlc.arg(node_ids)::bigint[]);

-- 转发 -------------------------------------------------------------------

-- name: ListForwards :many
SELECT * FROM forwards ORDER BY id;

-- name: ListEntryForwards :many
SELECT * FROM forwards WHERE entry_id = $1 ORDER BY hop;

-- name: ListForwardsOfEntries :many
SELECT * FROM forwards WHERE entry_id = ANY(sqlc.arg(entry_ids)::bigint[]) ORDER BY entry_id, hop;

-- name: ListMachineForwards :many
SELECT * FROM forwards WHERE machine_id = $1 ORDER BY listen_port;

-- name: ListManagedMachineForwards :many
-- 一台机器上应由 patato-node 执行的转发：入口已启用、不是手写规则。
SELECT f.* FROM forwards f JOIN node_entries e ON e.id = f.entry_id
WHERE f.machine_id = $1 AND f.mode <> 'manual' AND e.enabled
ORDER BY f.listen_port;

-- name: UsedForwardPorts :many
SELECT listen_port FROM forwards WHERE machine_id = $1;

-- name: CreateForward :one
INSERT INTO forwards (entry_id, hop, machine_id, listen_port, public_port, target_host, target_port, mode, target_ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: SetForwardEndpoints :exec
-- 机器对外地址、端口段或落地节点变化后重新计算的外网端口与下一跳。target_ip 随之更新（域名时清空，等待解析）。
UPDATE forwards SET public_port = $2, target_host = $3, target_port = $4, target_ip = $5, updated_at = now() WHERE id = $1;

-- name: SetForwardTargetIP :exec
UPDATE forwards SET target_ip = $2, updated_at = now() WHERE id = $1;

-- name: SetForwardReport :exec
-- patato-node 上报的应用结果。
UPDATE forwards SET status = sqlc.arg(status), applied_mode = sqlc.arg(applied_mode), error = sqlc.arg(error),
    applied_at = CASE WHEN sqlc.arg(status) = 'active' THEN COALESCE(sqlc.narg(applied_at), applied_at) ELSE applied_at END,
    reported_at = now()
WHERE id = sqlc.arg(id);

-- name: SetForwardProbe :one
-- 记录最近一次探测结果和 patato-node 统计的连续失败次数，返回之前的连续失败次数。
UPDATE forwards SET probe_ok = sqlc.arg(ok), probe_ms = sqlc.narg(ms), probe_at = sqlc.arg(at),
    probe_failures = sqlc.arg(failures)
FROM (SELECT o.id, o.probe_failures FROM forwards o WHERE o.id = sqlc.arg(id)::bigint FOR UPDATE) AS prev
WHERE forwards.id = prev.id
RETURNING prev.probe_failures AS previous_failures;

-- name: AddForwardProbe :exec
INSERT INTO forward_probe_hourly (forward_id, hour, ok, fail, latency_ms)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (forward_id, hour) DO UPDATE SET
    ok = forward_probe_hourly.ok + EXCLUDED.ok, fail = forward_probe_hourly.fail + EXCLUDED.fail,
    latency_ms = forward_probe_hourly.latency_ms + EXCLUDED.latency_ms;

-- name: ForwardProbeStats :many
-- 最近一段时间每条转发的探测成功率和平均延迟。
SELECT forward_id, sum(ok)::bigint AS ok, sum(fail)::bigint AS fail,
       CASE WHEN sum(ok) > 0 THEN (sum(latency_ms) / sum(ok))::bigint ELSE 0 END AS avg_ms
FROM forward_probe_hourly
WHERE hour >= sqlc.arg(since)::timestamptz AND forward_id = ANY(sqlc.arg(forward_ids)::bigint[])
GROUP BY forward_id;

-- name: DeleteForward :exec
DELETE FROM forwards WHERE id = $1;

-- name: DeleteEntryForwards :exec
DELETE FROM forwards WHERE entry_id = $1;

-- 线路流量 ---------------------------------------------------------------

-- name: AddForwardTrafficHourly :exec
INSERT INTO forward_traffic_hourly (forward_id, hour, route_id, machine_id, up, down)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (forward_id, hour) DO UPDATE SET
    up = forward_traffic_hourly.up + EXCLUDED.up, down = forward_traffic_hourly.down + EXCLUDED.down;

-- name: RollupForwardTrafficDaily :execrows
-- 把某一天（按时区）的小时数据重新汇总进天表；可重复执行，结果相同。
INSERT INTO forward_traffic_daily (forward_id, day, route_id, machine_id, up, down)
SELECT forward_id, sqlc.arg(day)::date, max(route_id), max(machine_id), sum(up)::bigint, sum(down)::bigint
FROM forward_traffic_hourly
WHERE hour >= sqlc.arg(from_ts)::timestamptz AND hour < sqlc.arg(to_ts)::timestamptz
GROUP BY forward_id
ON CONFLICT (forward_id, day) DO UPDATE SET up = EXCLUDED.up, down = EXCLUDED.down,
    route_id = EXCLUDED.route_id, machine_id = EXCLUDED.machine_id;

-- name: DeleteForwardTrafficHourlyBefore :exec
DELETE FROM forward_traffic_hourly WHERE hour < $1;

-- name: DeleteForwardProbeHourlyBefore :exec
DELETE FROM forward_probe_hourly WHERE hour < $1;

-- name: RouteTrafficSince :many
-- 每条线路自某时刻起的流量（小时表）。同一条线路的多跳各算一次，取第一跳（入口机器）的读数作为线路流量。
SELECT t.route_id::bigint AS route_id, sum(t.up)::bigint AS up, sum(t.down)::bigint AS down
FROM forward_traffic_hourly t
JOIN routes r ON r.id = t.route_id
WHERE t.hour >= sqlc.arg(since)::timestamptz AND t.machine_id = r.hops[1]
GROUP BY t.route_id;

-- name: RouteTrafficDaily :many
-- 每条线路每天的流量（天表），取第一跳读数。
SELECT t.route_id::bigint AS route_id, t.day, sum(t.up)::bigint AS up, sum(t.down)::bigint AS down
FROM forward_traffic_daily t
JOIN routes r ON r.id = t.route_id
WHERE t.day >= sqlc.arg(since)::date AND t.machine_id = r.hops[1]
GROUP BY t.route_id, t.day
ORDER BY t.route_id, t.day;

-- name: MachineForwardTrafficSince :many
-- 每台机器上所有转发自某时刻起的流量之和，可以和云厂商的流量额度对照。
SELECT machine_id, sum(up)::bigint AS up, sum(down)::bigint AS down
FROM forward_traffic_hourly
WHERE hour >= sqlc.arg(since)::timestamptz
GROUP BY machine_id;

-- name: ForwardTrafficSince :many
SELECT forward_id, sum(up)::bigint AS up, sum(down)::bigint AS down
FROM forward_traffic_hourly
WHERE hour >= sqlc.arg(since)::timestamptz
GROUP BY forward_id;

-- name: RouteZeroTrafficHours :many
-- route_idle 检测：最近 N 小时内线路第一跳没有任何流量的启用中转入口，以及该节点在这段时间的用户流量。
SELECT e.id AS entry_id, e.node_id, e.route_id::bigint AS route_id,
       COALESCE((SELECT sum(up + down) FROM forward_traffic_hourly t JOIN forwards f ON f.id = t.forward_id
                 WHERE f.entry_id = e.id AND f.hop = 0 AND t.hour >= sqlc.arg(since)::timestamptz), 0)::bigint AS relay_bytes,
       COALESCE((SELECT sum(up + down) FROM node_traffic_daily d
                 WHERE d.node_id = e.node_id AND d.day >= sqlc.arg(since)::timestamptz::date), 0)::bigint AS node_bytes
FROM node_entries e
WHERE e.enabled AND e.route_id IS NOT NULL AND e.created_at < sqlc.arg(since)::timestamptz
  AND EXISTS (SELECT 1 FROM forwards f WHERE f.entry_id = e.id AND f.mode <> 'manual');

-- name: SetEntryRoute :exec
-- 链路发现确认后，把自定义入口登记为经过某条线路（手写转发）。
UPDATE node_entries SET route_id = $2, updated_at = now(), version = version + 1 WHERE id = $1;

-- name: BumpEntryVersion :exec
UPDATE node_entries SET updated_at = now(), version = version + 1 WHERE id = $1;
