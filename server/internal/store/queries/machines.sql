-- 机器（patato-node）-----------------------------------------------------

-- name: ListMachines :many
SELECT * FROM machines ORDER BY host;

-- name: GetMachineByHost :one
SELECT * FROM machines WHERE host = $1;

-- name: GetMachineByTokenHash :one
SELECT * FROM machines WHERE token_hash = $1;

-- name: EnsureMachine :one
INSERT INTO machines (host) VALUES ($1)
ON CONFLICT (host) DO UPDATE SET host = EXCLUDED.host
RETURNING *;

-- name: SetMachineEnrollment :exec
UPDATE machines SET enroll_hash = $2, enroll_cipher = $3, enroll_expires_at = $4, updated_at = now()
WHERE id = $1;

-- name: ClaimMachineEnrollment :one
-- 用注册码换取机器 token：注册码一次性，换取后立即作废；旧的机器 token 同时失效。
UPDATE machines SET token_hash = sqlc.arg(token_hash), enroll_hash = NULL, enroll_cipher = NULL, enroll_expires_at = NULL,
    hostname = sqlc.arg(hostname), agent_version = sqlc.arg(agent_version), enrolled_at = now(), last_seen_at = now(),
    updated_at = now(), version = version + 1
WHERE enroll_hash = sqlc.arg(enroll_hash) AND enroll_expires_at > now()
RETURNING *;

-- name: EnrollMachineByAttestation :one
-- 凭 tolato 的机器身份证明接入：只接受正在接入的机器（换发 token），或安装窗口还开着的机器
-- （生成安装命令时打开，一小时内有效）。吊销后要重新生成安装命令才能再接入。
UPDATE machines SET token_hash = sqlc.arg(token_hash), enroll_hash = NULL, enroll_cipher = NULL, enroll_expires_at = NULL,
    hostname = sqlc.arg(hostname), agent_version = sqlc.arg(agent_version), enrolled_at = now(), last_seen_at = now(),
    updated_at = now(), version = version + 1
WHERE id = sqlc.arg(id) AND (token_hash IS NOT NULL OR enroll_expires_at > now())
RETURNING *;

-- name: RevokeMachine :exec
UPDATE machines SET token_hash = NULL, enroll_hash = NULL, enroll_cipher = NULL, enroll_expires_at = NULL,
    updated_at = now(), version = version + 1
WHERE id = $1;

-- name: TouchMachine :exec
UPDATE machines SET last_seen_at = now(), agent_version = $2, kernel = $3 WHERE id = $1;

-- name: SeenMachine :exec
UPDATE machines SET last_seen_at = now() WHERE id = $1;

-- name: ListNativeNodesOfHost :many
SELECT * FROM nodes WHERE host = $1 AND backend = 'native' ORDER BY id;

-- name: CountNodesOfHost :one
SELECT count(*) FROM nodes WHERE host = $1;

-- name: CountUsersOfHost :one
SELECT count(DISTINCT u.id)
FROM users u
JOIN plan_nodes pn ON pn.plan_id = u.plan_id
JOIN nodes n ON n.id = pn.node_id
WHERE n.host = $1 AND n.backend = 'native';

-- name: AdvanceMachineSpool :one
-- 序号大于已入账的最大序号才入账；返回 false 表示这一批已经入账过（重复上报）。
WITH up AS (
    INSERT INTO machine_spools (machine_id, spool, last_seq) VALUES ($1, $2, $3)
    ON CONFLICT (machine_id, spool) DO UPDATE SET last_seq = EXCLUDED.last_seq, updated_at = now()
    WHERE machine_spools.last_seq < EXCLUDED.last_seq
    RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM up) AS accepted;

-- name: SetNodeRuntime :exec
UPDATE nodes SET runtime = $2 WHERE id = $1;

-- name: RemoteDeviceCounts :many
-- 限制了设备数的用户在这台机器以外的节点上的在线设备数。
SELECT a.user_id, count(DISTINCT ip)::integer AS devices
FROM node_alive a
JOIN nodes n ON n.id = a.node_id, unnest(a.ips) AS ip
WHERE a.updated_at >= sqlc.arg(since)::timestamptz
  AND n.host <> sqlc.arg(host)::text
  AND a.user_id = ANY(sqlc.arg(user_ids)::bigint[])
GROUP BY a.user_id;

-- name: MachineVersionForUpdate :one
SELECT version FROM machines WHERE id = $1 FOR UPDATE;
