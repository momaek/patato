-- name: InsertTrafficReport :exec
INSERT INTO traffic_reports (node_id, rate, payload) VALUES ($1, $2, $3);

-- name: ClaimTrafficReports :many
SELECT * FROM traffic_reports
WHERE processed_at IS NULL
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkTrafficReportsProcessed :exec
UPDATE traffic_reports SET processed_at = now() WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteProcessedTrafficReports :exec
DELETE FROM traffic_reports WHERE processed_at IS NOT NULL AND processed_at < $1;

-- name: AddUserDailyTraffic :exec
INSERT INTO user_traffic_daily (user_id, day, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, day) DO UPDATE SET
    up = user_traffic_daily.up + EXCLUDED.up,
    down = user_traffic_daily.down + EXCLUDED.down;

-- name: AddNodeDailyTraffic :exec
INSERT INTO node_traffic_daily (node_id, day, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (node_id, day) DO UPDATE SET
    up = node_traffic_daily.up + EXCLUDED.up,
    down = node_traffic_daily.down + EXCLUDED.down;

-- name: NodeTrafficOnDay :many
SELECT * FROM node_traffic_daily WHERE day = $1;

-- name: TotalTrafficOnDay :one
SELECT COALESCE(sum(up + down), 0)::bigint FROM node_traffic_daily WHERE day = $1;

-- name: UserTrafficSince :many
SELECT * FROM user_traffic_daily WHERE user_id = $1 AND day >= $2 ORDER BY day;

-- name: ReplaceNodeAlive :exec
INSERT INTO node_alive (node_id, user_id, ips, updated_at) VALUES ($1, $2, $3, now())
ON CONFLICT (node_id, user_id) DO UPDATE SET ips = EXCLUDED.ips, updated_at = now();

-- name: DeleteStaleAlive :exec
DELETE FROM node_alive WHERE updated_at < $1;

-- name: AliveDeviceCounts :many
SELECT a.user_id, count(DISTINCT ip)::integer AS devices
FROM node_alive a, unnest(a.ips) AS ip
WHERE a.updated_at >= sqlc.arg(since)::timestamptz
  AND a.user_id = ANY(sqlc.arg(user_ids)::bigint[])
GROUP BY a.user_id;

-- name: UserDeviceCount :one
SELECT count(DISTINCT ip)::integer
FROM node_alive a, unnest(a.ips) AS ip
WHERE a.user_id = $1 AND a.updated_at >= $2;

-- name: CountOnlineUsers :one
SELECT count(DISTINCT user_id) FROM node_alive WHERE updated_at >= $1;

-- name: CountOnlineUsersByNode :many
SELECT node_id, count(*)::integer AS users FROM node_alive WHERE updated_at >= $1 GROUP BY node_id;
