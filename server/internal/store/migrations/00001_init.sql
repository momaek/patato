-- +goose Up

-- 运营者与凭证 ------------------------------------------------------------

CREATE TABLE operators (
    id               bigserial PRIMARY KEY,
    name             text        NOT NULL UNIQUE,
    webauthn_user_id bytea       NOT NULL UNIQUE,
    telegram_user_id bigint UNIQUE,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operator_invites (
    token_hash bytea PRIMARY KEY,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE TABLE webauthn_credentials (
    id           bytea PRIMARY KEY,
    operator_id  bigint      NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    credential   jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

CREATE TABLE webauthn_challenges (
    id          text PRIMARY KEY,
    operator_id bigint REFERENCES operators (id) ON DELETE CASCADE,
    purpose     text        NOT NULL,
    session     jsonb       NOT NULL,
    expires_at  timestamptz NOT NULL
);

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    kind       text        NOT NULL CHECK (kind IN ('operator', 'user')),
    subject_id bigint      NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    user_agent text        NOT NULL DEFAULT '',
    ip         text        NOT NULL DEFAULT ''
);
CREATE INDEX sessions_subject ON sessions (kind, subject_id);

CREATE TABLE api_tokens (
    id           bigserial PRIMARY KEY,
    operator_id  bigint      NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    token_hash   bytea       NOT NULL UNIQUE,
    scope        text        NOT NULL CHECK (scope IN ('read', 'write')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- patato auth login 的设备授权流程：CLI 发起，运营者在网页上批准，CLI 轮询取 token
CREATE TABLE cli_grants (
    id          text PRIMARY KEY,
    secret_hash bytea       NOT NULL,
    user_code   text        NOT NULL UNIQUE,
    scope       text        NOT NULL CHECK (scope IN ('read', 'write')),
    client_name text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    operator_id bigint REFERENCES operators (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);

-- 节点、模板、套餐 ---------------------------------------------------------

CREATE TABLE node_templates (
    id         bigserial PRIMARY KEY,
    code       text           NOT NULL UNIQUE,
    name       text           NOT NULL,
    region     text           NOT NULL DEFAULT '',
    protocol   text           NOT NULL,
    port_min   integer        NOT NULL,
    port_max   integer        NOT NULL,
    settings   jsonb          NOT NULL DEFAULT '{}',
    rate       numeric(6, 2)  NOT NULL DEFAULT 1,
    created_at timestamptz    NOT NULL DEFAULT now(),
    updated_at timestamptz    NOT NULL DEFAULT now(),
    version    bigint         NOT NULL DEFAULT 1,
    CHECK (port_min > 0 AND port_max >= port_min AND port_max <= 65535)
);

CREATE TABLE nodes (
    id                 bigserial PRIMARY KEY,
    code               text          NOT NULL UNIQUE,
    name               text          NOT NULL,
    template_id        bigint REFERENCES node_templates (id) ON DELETE SET NULL,
    host               text          NOT NULL DEFAULT '',
    address            text          NOT NULL,
    region             text          NOT NULL DEFAULT '',
    protocol           text          NOT NULL,
    port               integer       NOT NULL,
    server_port        integer       NOT NULL,
    settings           jsonb         NOT NULL DEFAULT '{}',
    rate               numeric(6, 2) NOT NULL DEFAULT 1,
    state              text          NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'online', 'offline')),
    tags               text[]        NOT NULL DEFAULT '{}',
    sort               integer       NOT NULL DEFAULT 0,
    key_hash           bytea UNIQUE,
    key_cipher         bytea,
    allow_legacy_token boolean       NOT NULL DEFAULT false,
    last_heartbeat_at  timestamptz,
    last_push_at       timestamptz,
    load               jsonb,
    created_at         timestamptz   NOT NULL DEFAULT now(),
    updated_at         timestamptz   NOT NULL DEFAULT now(),
    version            bigint        NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX nodes_host_server_port ON nodes (host, server_port) WHERE host <> '';

CREATE TABLE plans (
    id               bigserial PRIMARY KEY,
    code             text        NOT NULL UNIQUE,
    name             text        NOT NULL,
    transfer_bytes   bigint      NOT NULL CHECK (transfer_bytes >= 0),
    reset_method     text        NOT NULL DEFAULT 'purchase_day' CHECK (reset_method IN ('purchase_day', 'calendar_month', 'never')),
    device_limit     integer     NOT NULL DEFAULT 0,
    speed_limit_mbps integer     NOT NULL DEFAULT 0,
    prices           jsonb       NOT NULL DEFAULT '{}',
    sellable         boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    version          bigint      NOT NULL DEFAULT 1
);

CREATE TABLE plan_nodes (
    plan_id bigint NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    node_id bigint NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    PRIMARY KEY (plan_id, node_id)
);
CREATE INDEX plan_nodes_node ON plan_nodes (node_id);

-- 终端用户 ----------------------------------------------------------------

CREATE TABLE users (
    id               bigserial PRIMARY KEY,
    email            text        NOT NULL,
    password_hash    text        NOT NULL DEFAULT '',
    password_algo    text        NOT NULL DEFAULT 'argon2id',
    password_salt    text        NOT NULL DEFAULT '',
    uuid             text        NOT NULL UNIQUE,
    sub_token        text        NOT NULL UNIQUE,
    plan_id          bigint REFERENCES plans (id) ON DELETE SET NULL,
    banned           boolean     NOT NULL DEFAULT false,
    transfer_bytes   bigint      NOT NULL DEFAULT 0,
    used_up          bigint      NOT NULL DEFAULT 0,
    used_down        bigint      NOT NULL DEFAULT 0,
    expires_at       timestamptz,
    device_limit     integer,
    speed_limit_mbps integer,
    reset_anchor     timestamptz,
    next_reset_at    timestamptz,
    last_reset_at    timestamptz,
    token_reset_at   timestamptz,
    last_active_at   timestamptz,
    over_quota_at    timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    version          bigint      NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX users_email ON users (lower(email));
CREATE INDEX users_plan ON users (plan_id);
CREATE INDEX users_next_reset ON users (next_reset_at) WHERE next_reset_at IS NOT NULL;
CREATE INDEX users_expires ON users (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE password_resets (
    token_hash bytea PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE TABLE auth_failures (
    key          text PRIMARY KEY,
    failures     integer     NOT NULL DEFAULT 0,
    window_start timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz
);

-- 流量 --------------------------------------------------------------------

-- 节点上报原样落库，聚合异步进行
CREATE TABLE traffic_reports (
    id           bigserial PRIMARY KEY,
    node_id      bigint        NOT NULL,
    rate         numeric(6, 2) NOT NULL,
    payload      jsonb         NOT NULL,
    received_at  timestamptz   NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX traffic_reports_pending ON traffic_reports (id) WHERE processed_at IS NULL;

CREATE TABLE user_traffic_daily (
    user_id bigint NOT NULL,
    day     date   NOT NULL,
    up      bigint NOT NULL DEFAULT 0,
    down    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);

CREATE TABLE node_traffic_daily (
    node_id bigint NOT NULL,
    day     date   NOT NULL,
    up      bigint NOT NULL DEFAULT 0,
    down    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, day)
);

CREATE TABLE node_alive (
    node_id    bigint      NOT NULL,
    user_id    bigint      NOT NULL,
    ips        text[]      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, user_id)
);
CREATE INDEX node_alive_user ON node_alive (user_id);

-- 变更计划、审批、幂等、审计 ------------------------------------------------

CREATE TABLE change_plans (
    id            text PRIMARY KEY,
    kind          text        NOT NULL,
    params        jsonb       NOT NULL,
    steps         jsonb       NOT NULL,
    diff          jsonb       NOT NULL,
    impact        jsonb       NOT NULL,
    targets       jsonb       NOT NULL,
    summary       text        NOT NULL,
    declared_risk text        NOT NULL,
    risk          text        NOT NULL,
    risk_reason   text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned', 'pending_approval', 'applied', 'rejected', 'expired', 'stale', 'failed')),
    subject_kind  text        NOT NULL,
    subject_id    bigint      NOT NULL,
    subject_label text        NOT NULL DEFAULT '',
    actor_claim   text        NOT NULL DEFAULT '',
    entry         text        NOT NULL,
    instruction   text        NOT NULL DEFAULT '',
    result        jsonb,
    error         text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    applied_at    timestamptz
);
CREATE INDEX change_plans_status ON change_plans (status, created_at);

CREATE TABLE approvals (
    id                  text PRIMARY KEY,
    change_id           text        NOT NULL UNIQUE REFERENCES change_plans (id),
    status              text        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'voided')),
    expires_at          timestamptz NOT NULL,
    decided_by          bigint REFERENCES operators (id),
    decided_via         text        NOT NULL DEFAULT '',
    decided_at          timestamptz,
    reason              text        NOT NULL DEFAULT '',
    telegram_chat_id    bigint,
    telegram_message_id bigint,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approvals_status ON approvals (status, created_at);

CREATE TABLE idempotency_keys (
    subject      text        NOT NULL,
    key          text        NOT NULL,
    request_hash bytea       NOT NULL,
    status_code  integer     NOT NULL,
    response     jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subject, key)
);

CREATE TABLE audit_logs (
    id            bigserial PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    subject_kind  text        NOT NULL,
    subject_id    bigint      NOT NULL DEFAULT 0,
    subject_label text        NOT NULL DEFAULT '',
    actor_claim   text        NOT NULL DEFAULT '',
    entry         text        NOT NULL,
    action        text        NOT NULL,
    target        text        NOT NULL DEFAULT '',
    risk          text        NOT NULL DEFAULT '',
    change_id     text        NOT NULL DEFAULT '',
    result        text        NOT NULL,
    detail        jsonb       NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_logs_at ON audit_logs (at DESC);
CREATE INDEX audit_logs_target ON audit_logs (target, at DESC);

-- 事件与设置 --------------------------------------------------------------

CREATE TABLE events (
    id          bigserial PRIMARY KEY,
    kind        text        NOT NULL,
    severity    text        NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warning', 'critical')),
    subject     text        NOT NULL DEFAULT '',
    message     text        NOT NULL,
    payload     jsonb       NOT NULL DEFAULT '{}',
    dedup_key   text UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    notified_at timestamptz
);
CREATE INDEX events_open ON events (created_at DESC) WHERE resolved_at IS NULL;

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE settings;
DROP TABLE events;
DROP TABLE audit_logs;
DROP TABLE idempotency_keys;
DROP TABLE approvals;
DROP TABLE change_plans;
DROP TABLE node_alive;
DROP TABLE node_traffic_daily;
DROP TABLE user_traffic_daily;
DROP TABLE traffic_reports;
DROP TABLE auth_failures;
DROP TABLE password_resets;
DROP TABLE users;
DROP TABLE plan_nodes;
DROP TABLE plans;
DROP TABLE nodes;
DROP TABLE node_templates;
DROP TABLE cli_grants;
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE webauthn_challenges;
DROP TABLE webauthn_credentials;
DROP TABLE operator_invites;
DROP TABLE operators;
