-- 变更计划 ---------------------------------------------------------------

-- name: InsertChangePlan :exec
INSERT INTO change_plans (id, kind, params, steps, diff, impact, targets, summary, declared_risk, risk,
                          risk_reason, subject_kind, subject_id, subject_label, actor_claim, entry,
                          instruction, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18);

-- name: GetChangePlan :one
SELECT * FROM change_plans WHERE id = $1;

-- name: LockChangePlan :one
SELECT * FROM change_plans WHERE id = $1 FOR UPDATE;

-- name: SetChangePlanStatus :exec
UPDATE change_plans SET status = $2, result = $3, error = $4,
    applied_at = CASE WHEN $2 = 'applied' THEN now() ELSE applied_at END
WHERE id = $1;

-- name: ExpireChangePlans :exec
UPDATE change_plans SET status = 'expired'
WHERE status = 'planned' AND expires_at < now();

-- 审批 -------------------------------------------------------------------

-- name: CreateApproval :one
INSERT INTO approvals (id, change_id, expires_at) VALUES ($1, $2, $3) RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = $1;

-- name: GetApprovalByChange :one
SELECT * FROM approvals WHERE change_id = $1;

-- name: LockApproval :one
SELECT * FROM approvals WHERE id = $1 FOR UPDATE;

-- name: ListApprovals :many
SELECT a.*, c.summary, c.kind, c.risk, c.risk_reason, c.impact, c.subject_label, c.actor_claim, c.entry
FROM approvals a JOIN change_plans c ON c.id = a.change_id
WHERE (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
ORDER BY a.created_at DESC
LIMIT $1;

-- name: CountPendingApprovals :one
SELECT count(*) FROM approvals WHERE status = 'pending' AND expires_at > now();

-- name: DecideApproval :execrows
UPDATE approvals SET status = $2, decided_by = $3, decided_via = $4, reason = $5, decided_at = now()
WHERE id = $1 AND status = 'pending';

-- name: SetApprovalTelegram :exec
UPDATE approvals SET telegram_chat_id = $2, telegram_message_id = $3 WHERE id = $1;

-- name: ExpireApprovals :many
UPDATE approvals SET status = 'expired', decided_at = now()
WHERE status = 'pending' AND expires_at < now()
RETURNING id, change_id, telegram_chat_id, telegram_message_id;

-- 幂等 -------------------------------------------------------------------

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE subject = $1 AND key = $2;

-- name: SaveIdempotencyKey :exec
INSERT INTO idempotency_keys (subject, key, request_hash, status_code, response)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: DeleteOldIdempotencyKeys :exec
DELETE FROM idempotency_keys WHERE created_at < $1;

-- 审计 -------------------------------------------------------------------

-- name: InsertAudit :exec
INSERT INTO audit_logs (subject_kind, subject_id, subject_label, actor_claim, entry, action, target,
                        risk, change_id, result, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ListAudit :many
SELECT * FROM audit_logs
WHERE (sqlc.narg(q)::text IS NULL OR action ILIKE '%' || sqlc.narg(q)::text || '%'
       OR target ILIKE '%' || sqlc.narg(q)::text || '%' OR change_id = sqlc.narg(q)::text)
  AND (sqlc.narg(entry)::text IS NULL OR entry = sqlc.narg(entry)::text)
  AND (sqlc.narg(result)::text IS NULL OR result = sqlc.narg(result)::text)
  AND (sqlc.narg(target)::text IS NULL OR target = sqlc.narg(target)::text)
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- 事件 -------------------------------------------------------------------

-- name: InsertEvent :one
INSERT INTO events (kind, severity, subject, message, payload, dedup_key)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (dedup_key) DO NOTHING
RETURNING *;

-- name: ResolveEvents :exec
UPDATE events SET resolved_at = now(), dedup_key = NULL
WHERE dedup_key = $1 AND resolved_at IS NULL;

-- name: ListOpenEvents :many
SELECT * FROM events WHERE resolved_at IS NULL ORDER BY created_at DESC LIMIT $1;

-- name: ListEventsAfter :many
SELECT * FROM events WHERE id > $1 ORDER BY id LIMIT $2;

-- name: LatestEventID :one
SELECT COALESCE(max(id), 0)::bigint FROM events;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;

-- name: MarkEventNotified :exec
UPDATE events SET notified_at = now() WHERE id = $1;

-- 设置 -------------------------------------------------------------------

-- name: GetSetting :one
SELECT value FROM settings WHERE key = $1;

-- name: SetSetting :exec
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
