-- name: CreateOperator :one
INSERT INTO operators (name, webauthn_user_id)
VALUES ($1, $2)
RETURNING *;

-- name: GetOperator :one
SELECT * FROM operators WHERE id = $1;

-- name: GetOperatorByName :one
SELECT * FROM operators WHERE name = $1;

-- name: GetOperatorByWebAuthnID :one
SELECT * FROM operators WHERE webauthn_user_id = $1;

-- name: GetOperatorByTelegram :one
SELECT * FROM operators WHERE telegram_user_id = $1;

-- name: ListOperators :many
SELECT * FROM operators ORDER BY id;

-- name: CountOperators :one
SELECT count(*) FROM operators;

-- name: SetOperatorTelegram :exec
UPDATE operators SET telegram_user_id = $2 WHERE id = $1;

-- name: CreateOperatorInvite :exec
INSERT INTO operator_invites (token_hash, name, expires_at) VALUES ($1, $2, $3);

-- name: GetOperatorInvite :one
SELECT * FROM operator_invites
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now();

-- name: UseOperatorInvite :execrows
UPDATE operator_invites SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now();

-- name: AddWebAuthnCredential :exec
INSERT INTO webauthn_credentials (id, operator_id, credential) VALUES ($1, $2, $3);

-- name: ListWebAuthnCredentials :many
SELECT * FROM webauthn_credentials WHERE operator_id = $1 ORDER BY created_at;

-- name: UpdateWebAuthnCredential :exec
UPDATE webauthn_credentials SET credential = $2, last_used_at = now() WHERE id = $1;

-- name: SaveWebAuthnChallenge :exec
INSERT INTO webauthn_challenges (id, operator_id, purpose, session, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: TakeWebAuthnChallenge :one
DELETE FROM webauthn_challenges
WHERE id = $1 AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredChallenges :exec
DELETE FROM webauthn_challenges WHERE expires_at <= now();

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, kind, subject_id, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetSession :one
SELECT * FROM sessions WHERE token_hash = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteSubjectSessions :exec
DELETE FROM sessions WHERE kind = $1 AND subject_id = $2;

-- name: DeleteSubjectSessionsExcept :exec
DELETE FROM sessions WHERE kind = $1 AND subject_id = $2 AND token_hash <> $3;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= now();

-- name: CreateAPIToken :one
INSERT INTO api_tokens (operator_id, name, token_hash, scope)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAPITokenByHash :one
SELECT t.*, o.name AS operator_name
FROM api_tokens t JOIN operators o ON o.id = t.operator_id
WHERE t.token_hash = $1 AND t.revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListAPITokens :many
SELECT * FROM api_tokens WHERE operator_id = $1 AND revoked_at IS NULL ORDER BY id;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now()
WHERE id = $1 AND operator_id = $2 AND revoked_at IS NULL;

-- name: CreateCLIGrant :exec
INSERT INTO cli_grants (id, secret_hash, user_code, scope, client_name, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetCLIGrant :one
SELECT * FROM cli_grants WHERE id = $1;

-- name: GetCLIGrantByUserCode :one
SELECT * FROM cli_grants WHERE user_code = $1;

-- name: DecideCLIGrant :execrows
UPDATE cli_grants SET status = $2, operator_id = $3
WHERE id = $1 AND status = 'pending' AND expires_at > now();

-- name: ConsumeCLIGrant :one
UPDATE cli_grants SET status = 'consumed'
WHERE id = $1 AND status = 'approved' AND expires_at > now()
RETURNING *;

-- name: CreatePasswordReset :exec
INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3);

-- name: UsePasswordReset :one
UPDATE password_resets SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: GetAuthFailure :one
SELECT * FROM auth_failures WHERE key = $1;

-- name: RecordAuthFailure :one
INSERT INTO auth_failures (key, failures, window_start)
VALUES ($1, 1, now())
ON CONFLICT (key) DO UPDATE SET
    failures = CASE WHEN auth_failures.window_start < now() - make_interval(secs => sqlc.arg(window_seconds)::integer)
                    THEN 1 ELSE auth_failures.failures + 1 END,
    window_start = CASE WHEN auth_failures.window_start < now() - make_interval(secs => sqlc.arg(window_seconds)::integer)
                        THEN now() ELSE auth_failures.window_start END
RETURNING *;

-- name: LockAuthKey :exec
UPDATE auth_failures SET locked_until = $2 WHERE key = $1;

-- name: ClearAuthFailures :exec
DELETE FROM auth_failures WHERE key = $1;
