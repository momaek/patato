-- name: ListUsers :many
SELECT u.*,
       (CASE WHEN u.banned THEN 'banned'
             WHEN u.expires_at IS NOT NULL AND u.expires_at <= now() THEN 'expired'
             WHEN u.used_up + u.used_down >= u.transfer_bytes THEN 'over_quota'
             ELSE 'active' END)::text AS state,
       p.code AS plan_code, p.name AS plan_name
FROM users u LEFT JOIN plans p ON p.id = u.plan_id
WHERE (sqlc.narg(q)::text IS NULL
       OR u.email ILIKE '%' || sqlc.narg(q)::text || '%'
       OR u.id::text = sqlc.narg(q)::text)
  AND (sqlc.narg(state)::text IS NULL OR
       (CASE WHEN u.banned THEN 'banned'
             WHEN u.expires_at IS NOT NULL AND u.expires_at <= now() THEN 'expired'
             WHEN u.used_up + u.used_down >= u.transfer_bytes THEN 'over_quota'
             ELSE 'active' END) = sqlc.narg(state)::text)
  AND (sqlc.narg(expiring_before)::timestamptz IS NULL OR
       (u.expires_at > now() AND u.expires_at <= sqlc.narg(expiring_before)::timestamptz))
  AND (sqlc.narg(plan_id)::bigint IS NULL OR u.plan_id = sqlc.narg(plan_id)::bigint)
  AND u.id > sqlc.arg(after_id)::bigint
ORDER BY u.id
LIMIT sqlc.arg(row_limit)::integer;

-- name: UserStateCounts :one
SELECT count(*) AS total,
       count(*) FILTER (WHERE NOT banned AND (expires_at IS NULL OR expires_at > now())
                        AND used_up + used_down < transfer_bytes) AS active,
       count(*) FILTER (WHERE NOT banned AND expires_at IS NOT NULL AND expires_at <= now()) AS expired,
       count(*) FILTER (WHERE NOT banned AND (expires_at IS NULL OR expires_at > now())
                        AND used_up + used_down >= transfer_bytes) AS over_quota,
       count(*) FILTER (WHERE banned) AS banned
FROM users;

-- name: CountExpiringBetween :one
SELECT count(*) FROM users
WHERE NOT banned AND expires_at > sqlc.arg(from_at)::timestamptz AND expires_at <= sqlc.arg(to_at)::timestamptz;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: GetUserBySubToken :one
SELECT * FROM users WHERE sub_token = $1;

-- name: UserVersionForUpdate :one
SELECT version FROM users WHERE id = $1 FOR UPDATE;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, password_algo, uuid, sub_token, plan_id, transfer_bytes,
                   expires_at, device_limit, speed_limit_mbps, reset_anchor, next_reset_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateUserAdmin :one
UPDATE users SET
    plan_id = $2, transfer_bytes = $3, expires_at = $4, device_limit = $5,
    speed_limit_mbps = $6, banned = $7, reset_anchor = $8, next_reset_at = $9,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: ResetUserTraffic :exec
UPDATE users SET used_up = 0, used_down = 0, over_quota_at = NULL,
    last_reset_at = now(), next_reset_at = $2, updated_at = now(), version = version + 1
WHERE id = $1;

-- name: ResetUserSubscription :exec
UPDATE users SET sub_token = $2, uuid = $3, token_reset_at = now(),
    updated_at = now(), version = version + 1
WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, password_algo = $3, password_salt = '', updated_at = now()
WHERE id = $1;

-- name: TouchUserActive :exec
UPDATE users SET last_active_at = now() WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: AddUserTraffic :one
UPDATE users SET used_up = used_up + sqlc.arg(up)::bigint, used_down = used_down + sqlc.arg(down)::bigint,
    last_active_at = now()
WHERE id = sqlc.arg(id)::bigint
RETURNING id, email, used_up, used_down, transfer_bytes, over_quota_at;

-- name: SetUserOverQuota :exec
UPDATE users SET over_quota_at = $2 WHERE id = $1;

-- name: DueTrafficResets :many
SELECT id FROM users
WHERE next_reset_at IS NOT NULL AND next_reset_at <= now()
ORDER BY next_reset_at
LIMIT $1;

-- name: UsersExpiredSince :many
SELECT id, email, expires_at FROM users
WHERE NOT banned AND expires_at > sqlc.arg(since)::timestamptz AND expires_at <= now()
ORDER BY expires_at;

-- name: CountUsersInPlans :one
SELECT count(*) FROM users WHERE plan_id = ANY(sqlc.arg(plan_ids)::bigint[]);

-- name: SetPlanUsersTransfer :execrows
UPDATE users SET transfer_bytes = $2, updated_at = now(), version = version + 1 WHERE plan_id = $1;
