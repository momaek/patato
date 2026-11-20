-- 落地访问统计 ------------------------------------------------------------

-- name: AddDestinationStats :exec
-- 一个节点一个小时的一批条目，按主键累加。调用方保证批内 (user_id, kind, dest) 不重复。
INSERT INTO destination_stats_hourly (node_id, user_id, hour, kind, dest, up, down, conns, merged)
SELECT sqlc.arg(node_id)::bigint, unnest(sqlc.arg(user_ids)::bigint[]), sqlc.arg(hour)::timestamptz,
    unnest(sqlc.arg(kinds)::text[]), unnest(sqlc.arg(dests)::text[]),
    unnest(sqlc.arg(ups)::bigint[]), unnest(sqlc.arg(downs)::bigint[]),
    unnest(sqlc.arg(conns)::bigint[]), unnest(sqlc.arg(merged)::bigint[])
ON CONFLICT (node_id, user_id, hour, kind, dest) DO UPDATE SET
    up = destination_stats_hourly.up + EXCLUDED.up,
    down = destination_stats_hourly.down + EXCLUDED.down,
    conns = destination_stats_hourly.conns + EXCLUDED.conns,
    merged = destination_stats_hourly.merged + EXCLUDED.merged;

-- name: TopNodeDestinations :many
-- 节点视图：按用户求和，可只看一个用户。
SELECT kind, dest,
    sum(up)::bigint AS up, sum(down)::bigint AS down, sum(conns)::bigint AS conns,
    sum(merged)::bigint AS merged, count(DISTINCT user_id)::bigint AS users
FROM destination_stats_hourly
WHERE node_id = sqlc.arg(node_id) AND hour >= sqlc.arg(since)
  AND (sqlc.narg(user_id)::bigint IS NULL OR user_id = sqlc.narg(user_id))
GROUP BY kind, dest
ORDER BY sum(up + down) DESC, dest
LIMIT sqlc.arg(row_limit);

-- name: TopUserDestinations :many
-- 用户视图：可只看一个节点。
SELECT kind, dest,
    sum(up)::bigint AS up, sum(down)::bigint AS down, sum(conns)::bigint AS conns,
    sum(merged)::bigint AS merged, count(DISTINCT node_id)::bigint AS nodes
FROM destination_stats_hourly
WHERE user_id = sqlc.arg(user_id) AND hour >= sqlc.arg(since)
  AND (sqlc.narg(node_id)::bigint IS NULL OR node_id = sqlc.narg(node_id))
GROUP BY kind, dest
ORDER BY sum(up + down) DESC, dest
LIMIT sqlc.arg(row_limit);

-- name: DestinationTotals :one
-- 榜单所在范围的合计，用于显示占比。node_id 与 user_id 至少给一个。
SELECT COALESCE(sum(up), 0)::bigint AS up, COALESCE(sum(down), 0)::bigint AS down,
    COALESCE(sum(conns), 0)::bigint AS conns, count(DISTINCT (kind, dest))::bigint AS destinations
FROM destination_stats_hourly
WHERE hour >= sqlc.arg(since)
  AND (sqlc.narg(node_id)::bigint IS NULL OR node_id = sqlc.narg(node_id))
  AND (sqlc.narg(user_id)::bigint IS NULL OR user_id = sqlc.narg(user_id));

-- name: DeleteDestinationStatsBefore :execrows
DELETE FROM destination_stats_hourly WHERE hour < $1;
