-- 节点模板 ---------------------------------------------------------------

-- name: ListTemplates :many
SELECT * FROM node_templates ORDER BY code;

-- name: GetTemplate :one
SELECT * FROM node_templates WHERE id = $1;

-- name: GetTemplateByCode :one
SELECT * FROM node_templates WHERE code = $1;

-- name: CreateTemplate :one
INSERT INTO node_templates (code, name, region, protocol, port_min, port_max, settings, rate)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateTemplate :one
UPDATE node_templates SET
    name = $2, region = $3, protocol = $4, port_min = $5, port_max = $6,
    settings = $7, rate = $8, updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: TemplateVersionForUpdate :one
SELECT version FROM node_templates WHERE id = $1 FOR UPDATE;

-- 节点 -------------------------------------------------------------------

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY sort, id;

-- name: GetNode :one
SELECT * FROM nodes WHERE id = $1;

-- name: GetNodeByCode :one
SELECT * FROM nodes WHERE code = $1;

-- name: GetNodeByKeyHash :one
SELECT * FROM nodes WHERE key_hash = $1;

-- name: NodeVersionForUpdate :one
SELECT version FROM nodes WHERE id = $1 FOR UPDATE;

-- name: CreateNode :one
INSERT INTO nodes (code, name, template_id, host, address, region, protocol, port, server_port,
                   settings, rate, state, tags, key_hash, key_cipher, backend)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: SetNodeKey :exec
UPDATE nodes SET key_hash = $2, key_cipher = $3, updated_at = now(), version = version + 1
WHERE id = $1;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1;

-- name: BumpNodeVersion :exec
UPDATE nodes SET updated_at = now(), version = version + 1 WHERE id = $1;

-- name: TouchNodeHeartbeat :one
UPDATE nodes SET last_heartbeat_at = now(), state = 'online'
FROM (SELECT o.id, o.state FROM nodes o WHERE o.id = sqlc.arg(id)::bigint FOR UPDATE) AS prev
WHERE nodes.id = prev.id
RETURNING prev.state AS previous_state;

-- name: TouchNodePush :exec
UPDATE nodes SET last_push_at = now() WHERE id = $1;

-- name: SetNodeLoad :exec
UPDATE nodes SET load = $2 WHERE id = $1;

-- name: MarkStaleNodesOffline :many
UPDATE nodes SET state = 'offline'
WHERE state = 'online' AND last_heartbeat_at < $1
RETURNING id, code, name, last_heartbeat_at;

-- name: UsedServerPorts :many
SELECT server_port FROM nodes WHERE host = $1;

-- name: ListPlanNodePairs :many
SELECT pn.plan_id, pn.node_id, p.code AS plan_code, p.name AS plan_name
FROM plan_nodes pn JOIN plans p ON p.id = pn.plan_id
ORDER BY pn.plan_id, pn.node_id;

-- name: ListNodePlans :many
SELECT p.* FROM plans p JOIN plan_nodes pn ON pn.plan_id = p.id
WHERE pn.node_id = $1 ORDER BY p.id;

-- name: ListPlanNodes :many
SELECT n.* FROM nodes n JOIN plan_nodes pn ON pn.node_id = n.id
WHERE pn.plan_id = $1 ORDER BY n.sort, n.id;

-- name: AttachNode :execrows
INSERT INTO plan_nodes (plan_id, node_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DetachNode :execrows
DELETE FROM plan_nodes WHERE plan_id = $1 AND node_id = $2;

-- name: IsNodeAttached :one
SELECT EXISTS (SELECT 1 FROM plan_nodes WHERE plan_id = $1 AND node_id = $2);

-- name: CountUsersOfNode :one
SELECT count(*) FROM users u
WHERE u.plan_id IN (SELECT plan_id FROM plan_nodes WHERE node_id = $1);

-- name: AvailableUsersForNode :many
SELECT u.id, u.uuid,
       COALESCE(u.speed_limit_mbps, p.speed_limit_mbps)::integer AS speed_limit,
       COALESCE(u.device_limit, p.device_limit)::integer AS device_limit
FROM users u
JOIN plans p ON p.id = u.plan_id
JOIN plan_nodes pn ON pn.plan_id = p.id AND pn.node_id = $1
WHERE NOT u.banned
  AND u.used_up + u.used_down < u.transfer_bytes
  AND (u.expires_at IS NULL OR u.expires_at > now())
ORDER BY u.id;

-- 套餐 -------------------------------------------------------------------

-- name: ListPlans :many
SELECT p.*,
       (SELECT count(*) FROM users u WHERE u.plan_id = p.id) AS user_count,
       (SELECT count(*) FROM plan_nodes pn WHERE pn.plan_id = p.id) AS node_count
FROM plans p ORDER BY p.id;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1;

-- name: GetPlanByCode :one
SELECT * FROM plans WHERE code = $1;

-- name: PlanVersionForUpdate :one
SELECT version FROM plans WHERE id = $1 FOR UPDATE;

-- name: CreatePlan :one
INSERT INTO plans (code, name, transfer_bytes, reset_method, device_limit, speed_limit_mbps, prices, sellable)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdatePlan :one
UPDATE plans SET
    name = $2, transfer_bytes = $3, reset_method = $4, device_limit = $5,
    speed_limit_mbps = $6, prices = $7, sellable = $8,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: BumpPlanVersion :exec
UPDATE plans SET updated_at = now(), version = version + 1 WHERE id = $1;

-- name: DeletePlan :exec
DELETE FROM plans WHERE id = $1;

-- name: CountUsersInPlan :one
SELECT count(*) FROM users WHERE plan_id = $1;

-- name: UpdateNode :one
UPDATE nodes SET
    name = $2, address = $3, port = $4, server_port = $5, rate = $6, tags = $7, settings = $8,
    sort = $9, backend = $10, updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: CountNodesWithNamePrefix :one
SELECT count(*) FROM nodes WHERE name LIKE sqlc.arg(prefix)::text || '%';
