-- +goose Up

-- 原生节点协议（patato-node）----------------------------------------------

-- 一台机器跑一个 patato-node，承载机器上全部 backend = 'native' 的节点。机器以 nodes.host 关联。
CREATE TABLE machines (
    id                bigserial PRIMARY KEY,
    host              text        NOT NULL UNIQUE,
    token_hash        bytea UNIQUE,
    enroll_hash       bytea UNIQUE,
    enroll_cipher     bytea,
    enroll_expires_at timestamptz,
    hostname          text        NOT NULL DEFAULT '',
    agent_version     text        NOT NULL DEFAULT '',
    kernel            text        NOT NULL DEFAULT '',
    enrolled_at       timestamptz,
    last_seen_at      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    version           bigint      NOT NULL DEFAULT 1
);

-- 每个上报队列（spool）已入账的最大序号，用于精确去重。
CREATE TABLE machine_spools (
    machine_id bigint      NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    spool      text        NOT NULL,
    last_seq   bigint      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, spool)
);

ALTER TABLE nodes ADD COLUMN backend text NOT NULL DEFAULT 'uniproxy' CHECK (backend IN ('uniproxy', 'native'));
-- runtime 是 patato-node 上报的运行状态（内核是否在跑、端口、证书到期）。
ALTER TABLE nodes ADD COLUMN runtime jsonb;
CREATE INDEX nodes_native_host ON nodes (host) WHERE backend = 'native';

-- 节点配置或用户列表可能变化时通知已连接的 patato-node。NOTIFY 在事务提交时才送出，
-- 同一事务里的重复通知会合并。

-- +goose StatementBegin
CREATE FUNCTION patato_notify_nodes() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('patato_nodes', '');
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER users_notify_rows AFTER INSERT OR DELETE ON users
    FOR EACH STATEMENT EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER users_notify_update AFTER UPDATE ON users FOR EACH ROW
    WHEN (OLD.plan_id IS DISTINCT FROM NEW.plan_id
       OR OLD.uuid IS DISTINCT FROM NEW.uuid
       OR OLD.banned IS DISTINCT FROM NEW.banned
       OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
       OR OLD.speed_limit_mbps IS DISTINCT FROM NEW.speed_limit_mbps
       OR OLD.device_limit IS DISTINCT FROM NEW.device_limit
       OR (OLD.used_up + OLD.used_down < OLD.transfer_bytes) IS DISTINCT FROM (NEW.used_up + NEW.used_down < NEW.transfer_bytes))
    EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER plan_nodes_notify AFTER INSERT OR UPDATE OR DELETE ON plan_nodes
    FOR EACH STATEMENT EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER plans_notify AFTER UPDATE ON plans FOR EACH ROW
    WHEN (OLD.speed_limit_mbps IS DISTINCT FROM NEW.speed_limit_mbps OR OLD.device_limit IS DISTINCT FROM NEW.device_limit)
    EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER nodes_notify_rows AFTER INSERT OR DELETE ON nodes
    FOR EACH STATEMENT EXECUTE FUNCTION patato_notify_nodes();
CREATE TRIGGER nodes_notify_update AFTER UPDATE ON nodes FOR EACH ROW
    WHEN (OLD.version IS DISTINCT FROM NEW.version OR OLD.host IS DISTINCT FROM NEW.host OR OLD.backend IS DISTINCT FROM NEW.backend)
    EXECUTE FUNCTION patato_notify_nodes();

-- +goose Down
DROP TRIGGER nodes_notify_update ON nodes;
DROP TRIGGER nodes_notify_rows ON nodes;
DROP TRIGGER plans_notify ON plans;
DROP TRIGGER plan_nodes_notify ON plan_nodes;
DROP TRIGGER users_notify_update ON users;
DROP TRIGGER users_notify_rows ON users;
DROP FUNCTION patato_notify_nodes();
DROP INDEX nodes_native_host;
ALTER TABLE nodes DROP COLUMN runtime;
ALTER TABLE nodes DROP COLUMN backend;
DROP TABLE machine_spools;
DROP TABLE machines;
