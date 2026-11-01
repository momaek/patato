-- 分流规则 ---------------------------------------------------------------

-- name: ListRuleProfiles :many
SELECT r.*,
       (SELECT count(*) FROM plans p WHERE p.rule_profile = r.code) AS plan_count,
       (SELECT count(*) FROM users u WHERE u.rule_profile = r.code) AS user_count
FROM rule_profiles r ORDER BY r.id;

-- name: ListSelectableRuleProfiles :many
SELECT * FROM rule_profiles WHERE user_selectable ORDER BY id;

-- name: GetRuleProfile :one
SELECT * FROM rule_profiles WHERE id = $1;

-- name: GetRuleProfileByCode :one
SELECT * FROM rule_profiles WHERE code = $1;

-- name: RuleProfileVersionForUpdate :one
SELECT version FROM rule_profiles WHERE id = $1 FOR UPDATE;

-- name: CreateRuleProfile :one
INSERT INTO rule_profiles (code, name, description, source_url, content, user_selectable, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateRuleProfile :one
UPDATE rule_profiles SET
    name = $2, description = $3, source_url = $4, content = $5, user_selectable = $6,
    fetched_at = $7, fetch_error = '', updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: SetRuleProfileFetched :exec
UPDATE rule_profiles SET content = $2, fetched_at = now(), fetch_error = '' WHERE id = $1;

-- name: SetRuleProfileFetchError :exec
UPDATE rule_profiles SET fetch_error = $2 WHERE id = $1;

-- name: DeleteRuleProfile :exec
DELETE FROM rule_profiles WHERE id = $1;

-- name: CountRuleProfileUsers :one
-- 实际使用这份规则的用户：自己选了它，或没自选、跟随的套餐默认是它。
SELECT count(*) FROM users u
LEFT JOIN plans p ON p.id = u.plan_id
WHERE u.rule_profile = sqlc.arg(code)::text OR (u.rule_profile IS NULL AND p.rule_profile = sqlc.arg(code)::text);

-- name: SetPlanRuleProfile :exec
UPDATE plans SET rule_profile = $2 WHERE id = $1;

-- name: SetUserRuleProfile :exec
UPDATE users SET rule_profile = $2, updated_at = now(), version = version + 1 WHERE id = $1;

-- 规则文件缓存 -------------------------------------------------------------

-- name: EnsureRuleList :exec
INSERT INTO rule_lists (key, url) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: GetRuleList :one
SELECT * FROM rule_lists WHERE key = $1;

-- name: ListRuleLists :many
SELECT key, url, fetched_at, fetch_error FROM rule_lists ORDER BY key;

-- name: SetRuleListContent :exec
UPDATE rule_lists SET content = $2, fetched_at = now(), fetch_error = '' WHERE key = $1;

-- name: SetRuleListError :exec
UPDATE rule_lists SET fetch_error = $2 WHERE key = $1;

-- name: DeleteRuleListsExcept :execrows
DELETE FROM rule_lists WHERE NOT (key = ANY(sqlc.arg(keys)::text[]));
