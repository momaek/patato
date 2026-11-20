-- +goose Up

-- 中转线路：线路（routes）是有序的中转机器列表，入口（node_entries）是节点的一个对外地址，
-- 转发（forwards）是入口在某一跳上的一条规则。设计见 docs/relay.md 与 docs/monitoring.md。

-- 机器的网络属性 ------------------------------------------------------------
-- public_address：别的机器和客户端连它用的地址，空串表示用 tolato 上报的 IP；域名由服务端解析后下发。
-- port_ranges：可分配的端口段，[{"local": "40000-49999"}, {"local": "20000-20099", "public_from": 30000}]；
--   空数组表示默认段 40000-49999。nat 为 true 时必须填写，Patato 只在这些段里分配。
-- reserved_ports：机器上现有规则（手写 DNAT、socat）占用的端口段，分配时避开，格式同 ["60000-62000"]。
-- listening_ports：patato-node 上报的本机正在监听的端口，分配时避开。
-- state：中转跳的在线状态。接入了 patato-node 的机器看它的心跳，没接入的看 tolato；都不知道时为 unknown（不撤入口）。
ALTER TABLE machines
    ADD COLUMN public_address   text        NOT NULL DEFAULT '',
    ADD COLUMN port_ranges      jsonb       NOT NULL DEFAULT '[]',
    ADD COLUMN nat              boolean     NOT NULL DEFAULT false,
    ADD COLUMN forward_mode     text        NOT NULL DEFAULT 'auto' CHECK (forward_mode IN ('auto', 'nft', 'userspace')),
    ADD COLUMN reserved_ports   jsonb       NOT NULL DEFAULT '[]',
    ADD COLUMN listening_ports  integer[]   NOT NULL DEFAULT '{}',
    ADD COLUMN tolato_ip        text        NOT NULL DEFAULT '',
    ADD COLUMN tolato_online    boolean,
    ADD COLUMN state            text        NOT NULL DEFAULT 'unknown' CHECK (state IN ('unknown', 'online', 'offline')),
    ADD COLUMN state_changed_at timestamptz,
    -- relay_runtime 是 patato-node 上报的转发运行状态：实际模式、前置条件（ip_forward）错误
    ADD COLUMN relay_runtime    jsonb,
    ADD CONSTRAINT machines_nat_needs_ranges CHECK (NOT nat OR port_ranges <> '[]'::jsonb);

-- 线路 -------------------------------------------------------------------
CREATE TABLE routes (
    id         bigserial PRIMARY KEY,
    code       text        NOT NULL UNIQUE,
    name       text        NOT NULL,
    -- hops 是 machines.id 的有序列表：第一跳是客户端连接的入口机器
    hops       bigint[]    NOT NULL CHECK (cardinality(hops) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version    bigint      NOT NULL DEFAULT 1
);

-- 入口 -------------------------------------------------------------------
-- 没有任何入口的节点等价于一个启用的直连入口。
-- route_id 为空时是直连入口（address 为空）或自定义入口（address/port 非空，例如购买的 IPLC，状态只跟随节点）。
-- 中转入口的 address/port 非空时覆盖订阅里的第一跳地址（例如给客户端的域名）。
-- name 非空时订阅里原样使用（Xboard 导入的子节点名），否则为「节点名 · 后缀」。
CREATE TABLE node_entries (
    id         bigserial PRIMARY KEY,
    node_id    bigint      NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    route_id   bigint REFERENCES routes (id) ON DELETE RESTRICT,
    suffix     text        NOT NULL DEFAULT '',
    name       text        NOT NULL DEFAULT '',
    address    text        NOT NULL DEFAULT '',
    port       integer     NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
    enabled    boolean     NOT NULL DEFAULT true,
    sort       integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version    bigint      NOT NULL DEFAULT 1
);
CREATE INDEX node_entries_node ON node_entries (node_id);
CREATE INDEX node_entries_route ON node_entries (route_id) WHERE route_id IS NOT NULL;

-- 转发 -------------------------------------------------------------------
-- 监听本机端口 listen_port；上一跳和客户端连 public_port（NAT 映射时与 listen_port 不同）。
-- mode：manual 是运营者手写的规则，只登记不下发、不计流量；nft / userspace 由 patato-node 执行。
-- target_host 是下一跳地址（IP 或域名），域名由服务端解析成 IP 后下发。
CREATE TABLE forwards (
    id             bigserial PRIMARY KEY,
    entry_id       bigint      NOT NULL REFERENCES node_entries (id) ON DELETE CASCADE,
    hop            integer     NOT NULL CHECK (hop >= 0),
    machine_id     bigint      NOT NULL REFERENCES machines (id) ON DELETE RESTRICT,
    listen_port    integer     NOT NULL CHECK (listen_port BETWEEN 1 AND 65535),
    public_port    integer     NOT NULL CHECK (public_port BETWEEN 1 AND 65535),
    target_host    text        NOT NULL,
    target_port    integer     NOT NULL CHECK (target_port BETWEEN 1 AND 65535),
    -- target_ip 是服务端最近一次把 target_host 解析出的 IP，即实际下发给 patato-node 的地址
    target_ip      text        NOT NULL DEFAULT '',
    mode           text        NOT NULL CHECK (mode IN ('manual', 'nft', 'userspace')),
    status         text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'failed')),
    applied_mode   text        NOT NULL DEFAULT '',
    applied_at     timestamptz,
    error          text        NOT NULL DEFAULT '',
    reported_at    timestamptz,
    probe_ok       boolean,
    probe_ms       integer,
    probe_at       timestamptz,
    -- probe_failures 是连续 TCP 探测失败次数，达到 3 次时入口撤下
    probe_failures integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (machine_id, listen_port),
    UNIQUE (entry_id, hop)
);
CREATE INDEX forwards_machine ON forwards (machine_id);

-- 线路流量：按线上字节统计。route_id、machine_id 冗余保存，入口删除后线路和机器的历史流量仍在。
-- 小时表保留 30 天，天表长期保留。
CREATE TABLE forward_traffic_hourly (
    forward_id bigint      NOT NULL,
    hour       timestamptz NOT NULL,
    route_id   bigint,
    machine_id bigint      NOT NULL,
    up         bigint      NOT NULL DEFAULT 0,
    down       bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (forward_id, hour)
);
CREATE INDEX forward_traffic_hourly_hour ON forward_traffic_hourly (hour);

CREATE TABLE forward_traffic_daily (
    forward_id bigint NOT NULL,
    day        date   NOT NULL,
    route_id   bigint,
    machine_id bigint NOT NULL,
    up         bigint NOT NULL DEFAULT 0,
    down       bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (forward_id, day)
);
CREATE INDEX forward_traffic_daily_route ON forward_traffic_daily (route_id, day);

-- 链路探测：每台中转机器每分钟对每条转发的下一跳 TCP 建连一次，按小时汇总成功、失败次数和成功时的耗时之和。
CREATE TABLE forward_probe_hourly (
    forward_id bigint      NOT NULL,
    hour       timestamptz NOT NULL,
    ok         integer     NOT NULL DEFAULT 0,
    fail       integer     NOT NULL DEFAULT 0,
    latency_ms bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (forward_id, hour)
);

-- 转发列表变化时通知 patato-node 同步（与节点同一个 NOTIFY 通道）。上报引起的状态列变化不通知。
CREATE TRIGGER forwards_notify_rows AFTER INSERT OR DELETE ON forwards
    FOR EACH STATEMENT EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER forwards_notify_update AFTER UPDATE ON forwards FOR EACH ROW
    WHEN (OLD.target_host IS DISTINCT FROM NEW.target_host OR OLD.target_ip IS DISTINCT FROM NEW.target_ip OR OLD.target_port IS DISTINCT FROM NEW.target_port
       OR OLD.listen_port IS DISTINCT FROM NEW.listen_port OR OLD.mode IS DISTINCT FROM NEW.mode)
    EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER node_entries_notify AFTER INSERT OR DELETE OR UPDATE OF enabled ON node_entries
    FOR EACH STATEMENT EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER machines_notify_relay AFTER UPDATE ON machines FOR EACH ROW
    WHEN (OLD.forward_mode IS DISTINCT FROM NEW.forward_mode)
    EXECUTE FUNCTION patato_notify_nodes();

-- +goose Down
DROP TRIGGER machines_notify_relay ON machines;
DROP TRIGGER node_entries_notify ON node_entries;
DROP TRIGGER forwards_notify_update ON forwards;
DROP TRIGGER forwards_notify_rows ON forwards;
DROP TABLE forward_probe_hourly;
DROP TABLE forward_traffic_daily;
DROP TABLE forward_traffic_hourly;
DROP TABLE forwards;
DROP TABLE node_entries;
DROP TABLE routes;
ALTER TABLE machines
    DROP CONSTRAINT machines_nat_needs_ranges,
    DROP COLUMN relay_runtime,
    DROP COLUMN state_changed_at,
    DROP COLUMN state,
    DROP COLUMN tolato_online,
    DROP COLUMN tolato_ip,
    DROP COLUMN listening_ports,
    DROP COLUMN reserved_ports,
    DROP COLUMN forward_mode,
    DROP COLUMN nat,
    DROP COLUMN port_ranges,
    DROP COLUMN public_address;
