-- +goose Up

-- 落地访问统计：patato-node 按「节点 × 用户 × 目标」上报的聚合流量，按小时累加。
-- 节点视图是按用户求和的结果，不单独存储。默认关闭（monitoring.destinations.enabled），按 retention_days 清理。
-- kind：domain（可注册域名）、ip，或 other（超出每节点每周期条目上限后合并的「其他」）。
-- merged 只在 other 条目上有值：被合并进来的目标数。
CREATE TABLE destination_stats_hourly (
    node_id bigint      NOT NULL,
    user_id bigint      NOT NULL,
    hour    timestamptz NOT NULL,
    kind    text        NOT NULL CHECK (kind IN ('domain', 'ip', 'other')),
    dest    text        NOT NULL,
    up      bigint      NOT NULL DEFAULT 0,
    down    bigint      NOT NULL DEFAULT 0,
    conns   bigint      NOT NULL DEFAULT 0,
    merged  bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, user_id, hour, kind, dest)
);
CREATE INDEX destination_stats_hourly_node_hour ON destination_stats_hourly (node_id, hour);
CREATE INDEX destination_stats_hourly_user_hour ON destination_stats_hourly (user_id, hour);
CREATE INDEX destination_stats_hourly_hour ON destination_stats_hourly (hour);

-- +goose Down
DROP TABLE destination_stats_hourly;
