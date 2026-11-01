-- Webhook ----------------------------------------------------------------

-- name: CreateWebhook :one
INSERT INTO webhooks (name, url, secret_cipher, kinds, enabled) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = $1;

-- name: WebhookVersionForUpdate :one
SELECT version FROM webhooks WHERE id = $1 FOR UPDATE;

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY id;

-- name: WebhooksForEvent :many
SELECT * FROM webhooks WHERE enabled AND (cardinality(kinds) = 0 OR sqlc.arg(kind)::text = ANY(kinds)) ORDER BY id;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE id = $1;

-- name: SetWebhookDelivery :exec
UPDATE webhooks SET last_status = $2, last_delivery_at = now() WHERE id = $1;

-- 自动处置规则 -----------------------------------------------------------

-- name: CreateAutomationRule :one
INSERT INTO automation_rules (name, event_kind, action, params, enabled) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetAutomationRule :one
SELECT * FROM automation_rules WHERE id = $1;

-- name: AutomationRuleVersionForUpdate :one
SELECT version FROM automation_rules WHERE id = $1 FOR UPDATE;

-- name: ListAutomationRules :many
SELECT * FROM automation_rules ORDER BY id;

-- name: RulesForEvent :many
SELECT * FROM automation_rules WHERE enabled AND event_kind = $1 ORDER BY id;

-- name: UpdateAutomationRule :one
UPDATE automation_rules SET name = $2, params = $3, enabled = $4, updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: DeleteAutomationRule :exec
DELETE FROM automation_rules WHERE id = $1;

-- name: SetAutomationResult :exec
UPDATE automation_rules SET last_fired_at = now(), last_result = $2 WHERE id = $1;

-- 多人审批与一次性码 -----------------------------------------------------

-- name: AddApprovalVote :execrows
INSERT INTO approval_votes (approval_id, operator_id, via) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: ListApprovalVotes :many
SELECT v.*, o.name AS operator_name
FROM approval_votes v JOIN operators o ON o.id = v.operator_id
WHERE v.approval_id = $1 ORDER BY v.created_at;

-- name: UpsertApprovalCode :exec
INSERT INTO approval_codes (approval_id, operator_id, code_hash, attempts, expires_at)
VALUES ($1, $2, $3, 0, $4)
ON CONFLICT (approval_id, operator_id) DO UPDATE SET code_hash = EXCLUDED.code_hash, attempts = 0, expires_at = EXCLUDED.expires_at;

-- name: GetApprovalCode :one
SELECT * FROM approval_codes WHERE approval_id = $1 AND operator_id = $2 FOR UPDATE;

-- name: BumpApprovalCodeAttempts :exec
UPDATE approval_codes SET attempts = attempts + 1 WHERE approval_id = $1 AND operator_id = $2;

-- name: DeleteApprovalCode :exec
DELETE FROM approval_codes WHERE approval_id = $1 AND operator_id = $2;

-- 套餐版本 ---------------------------------------------------------------

-- name: InsertPlanRevision :exec
INSERT INTO plan_revisions (plan_id, version, snapshot) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: ListPlanRevisions :many
SELECT * FROM plan_revisions WHERE plan_id = $1 ORDER BY version DESC LIMIT 50;

-- name: SetPlanRollout :exec
UPDATE plans SET rollout_prices = $2, rollout_percent = $3, updated_at = now() WHERE id = $1;

-- name: SetNodeMonthlyCost :exec
UPDATE nodes SET monthly_cost = $2 WHERE id = $1;

-- 报表 -------------------------------------------------------------------

-- name: DailyTrafficTotals :many
SELECT day, COALESCE(sum(up), 0)::bigint AS up, COALESCE(sum(down), 0)::bigint AS down
FROM node_traffic_daily WHERE day >= $1 GROUP BY day ORDER BY day;

-- name: TopUsersByTraffic :many
SELECT t.user_id, u.email, COALESCE(sum(t.up + t.down), 0)::bigint AS bytes
FROM user_traffic_daily t JOIN users u ON u.id = t.user_id
WHERE t.day >= $1 GROUP BY t.user_id, u.email ORDER BY bytes DESC LIMIT $2;

-- name: NodeTrafficSince :many
SELECT node_id, COALESCE(sum(up + down), 0)::bigint AS bytes FROM node_traffic_daily WHERE day >= $1 GROUP BY node_id;

-- name: DailyRevenue :many
SELECT (paid_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, count(*) AS orders,
       COALESCE(sum(total), 0)::bigint AS revenue, COALESCE(sum(balance_used), 0)::bigint AS balance_used,
       COALESCE(sum(discount), 0)::bigint AS discount
FROM orders WHERE paid_at >= sqlc.arg(since)::timestamptz AND status IN ('paid', 'refunded')
GROUP BY 1 ORDER BY 1;

-- name: DailyRefunds :many
SELECT (refunded_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, count(*) AS orders,
       COALESCE(sum(refund_amount), 0)::bigint AS amount
FROM orders WHERE refunded_at >= sqlc.arg(since)::timestamptz AND status = 'refunded'
GROUP BY 1 ORDER BY 1;

-- name: RevenueByPlan :many
SELECT COALESCE(plan_name, '')::text AS plan_name, count(*) AS orders, COALESCE(sum(total), 0)::bigint AS revenue
FROM orders WHERE paid_at >= $1 AND status IN ('paid', 'refunded')
GROUP BY plan_name ORDER BY revenue DESC;

-- name: UserTrafficAnomalies :many
-- 统计日用量超过前 7 天日均 factor 倍、且超过 min_bytes 的用户
WITH day_usage AS (
    SELECT user_id, (up + down)::bigint AS bytes FROM user_traffic_daily WHERE day = sqlc.arg(day)::date
), baseline AS (
    SELECT user_id, avg(up + down)::bigint AS avg_bytes FROM user_traffic_daily
    WHERE day >= sqlc.arg(day)::date - 7 AND day < sqlc.arg(day)::date GROUP BY user_id
)
SELECT d.user_id, u.email, d.bytes, COALESCE(b.avg_bytes, 0)::bigint AS avg_bytes
FROM day_usage d JOIN users u ON u.id = d.user_id LEFT JOIN baseline b ON b.user_id = d.user_id
WHERE d.bytes >= sqlc.arg(min_bytes)::bigint
  AND d.bytes > COALESCE(b.avg_bytes, 0) * sqlc.arg(factor)::bigint
ORDER BY d.bytes DESC LIMIT 100;

-- name: UsersExpiringBetween :many
SELECT id, email, expires_at FROM users
WHERE NOT banned AND expires_at > sqlc.arg(from_at)::timestamptz AND expires_at <= sqlc.arg(to_at)::timestamptz
ORDER BY expires_at LIMIT 1000;

-- name: ListAuditRange :many
SELECT * FROM audit_logs
WHERE (sqlc.narg(q)::text IS NULL OR action ILIKE '%' || sqlc.narg(q)::text || '%'
       OR target ILIKE '%' || sqlc.narg(q)::text || '%' OR change_id = sqlc.narg(q)::text)
  AND (sqlc.narg(entry)::text IS NULL OR entry = sqlc.narg(entry)::text)
  AND (sqlc.narg(result)::text IS NULL OR result = sqlc.narg(result)::text)
  AND (sqlc.narg(target)::text IS NULL OR target = sqlc.narg(target)::text)
  AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR at < sqlc.narg(until)::timestamptz)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit)::integer;
