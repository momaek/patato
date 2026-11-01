-- +goose Up

-- 二期：售卖与运营（订单、支付、优惠券、邀请返佣、工单）、Webhook 与自动处置、多人审批。
-- 金额一律以分存储。

-- 用户：余额（返佣入账后可在续费时抵扣）、邀请关系、注册来源 -------------------

ALTER TABLE users ADD COLUMN balance bigint NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN invite_code text;
ALTER TABLE users ADD COLUMN invited_by bigint REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN register_ip text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX users_invite_code ON users (invite_code) WHERE invite_code IS NOT NULL;
CREATE INDEX users_invited_by ON users (invited_by, created_at) WHERE invited_by IS NOT NULL;

-- 节点成本（分 / 月），用于成本报表
ALTER TABLE nodes ADD COLUMN monthly_cost bigint NOT NULL DEFAULT 0 CHECK (monthly_cost >= 0);

-- 套餐灰度调价：rollout_percent% 的用户看到并按 rollout_prices 下单，100% 后由运营者转正
ALTER TABLE plans ADD COLUMN rollout_prices jsonb;
ALTER TABLE plans ADD COLUMN rollout_percent integer NOT NULL DEFAULT 0 CHECK (rollout_percent BETWEEN 0 AND 100);

-- 套餐版本：每次修改前保存一份快照
CREATE TABLE plan_revisions (
    id         bigserial PRIMARY KEY,
    plan_id    bigint      NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    version    bigint      NOT NULL,
    snapshot   jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (plan_id, version)
);

-- 优惠券 ------------------------------------------------------------------

CREATE TABLE coupons (
    id         bigserial PRIMARY KEY,
    code       text        NOT NULL,
    name       text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('amount', 'percent')),
    -- amount：减免金额（分）；percent：减免百分比（1–100）
    value      bigint      NOT NULL CHECK (value > 0),
    plan_ids   bigint[]    NOT NULL DEFAULT '{}',
    periods    text[]      NOT NULL DEFAULT '{}',
    max_uses   integer     NOT NULL DEFAULT 0,
    per_user   integer     NOT NULL DEFAULT 0,
    used       integer     NOT NULL DEFAULT 0,
    starts_at  timestamptz,
    ends_at    timestamptz,
    enabled    boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version    bigint      NOT NULL DEFAULT 1,
    CHECK (kind <> 'percent' OR value <= 100)
);
CREATE UNIQUE INDEX coupons_code ON coupons (lower(code));

-- 订单 --------------------------------------------------------------------

CREATE TABLE orders (
    id               bigserial PRIMARY KEY,
    trade_no         text        NOT NULL UNIQUE,
    user_id          bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    plan_id          bigint REFERENCES plans (id) ON DELETE SET NULL,
    plan_name        text        NOT NULL,
    period           text        NOT NULL,
    kind             text        NOT NULL CHECK (kind IN ('new', 'renew', 'change', 'reset_traffic')),
    amount           bigint      NOT NULL CHECK (amount >= 0),
    discount         bigint      NOT NULL DEFAULT 0 CHECK (discount >= 0),
    balance_used     bigint      NOT NULL DEFAULT 0 CHECK (balance_used >= 0),
    total            bigint      NOT NULL CHECK (total >= 0),
    coupon_id        bigint REFERENCES coupons (id) ON DELETE SET NULL,
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'refunded', 'cancelled')),
    method           text        NOT NULL DEFAULT '',
    gateway_trade_no text        NOT NULL DEFAULT '',
    -- 支付后实际顺延的天数和顺延前的到期时间，退款时据此扣回
    days             integer     NOT NULL DEFAULT 0,
    prev_expires_at  timestamptz,
    refund_amount    bigint      NOT NULL DEFAULT 0,
    refund_days      integer     NOT NULL DEFAULT 0,
    note             text        NOT NULL DEFAULT '',
    source           text        NOT NULL DEFAULT 'portal' CHECK (source IN ('portal', 'admin', 'xboard')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    paid_at          timestamptz,
    cancelled_at     timestamptz,
    refunded_at      timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    version          bigint      NOT NULL DEFAULT 1
);
CREATE INDEX orders_user ON orders (user_id, created_at DESC);
CREATE INDEX orders_status ON orders (status, created_at DESC);
CREATE INDEX orders_paid ON orders (paid_at) WHERE paid_at IS NOT NULL;
CREATE INDEX orders_coupon ON orders (coupon_id, user_id) WHERE coupon_id IS NOT NULL;

-- 支付回调原样留存，验签失败的也记，便于对账和排查
CREATE TABLE payment_callbacks (
    id          bigserial PRIMARY KEY,
    method      text        NOT NULL,
    trade_no    text        NOT NULL DEFAULT '',
    verified    boolean     NOT NULL,
    result      text        NOT NULL,
    payload     jsonb       NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX payment_callbacks_trade ON payment_callbacks (trade_no, received_at DESC);

-- 邀请返佣：被邀请人付款后进入待入账，等待期内没有退款才入账；命中风控规则的冻结 ------

CREATE TABLE commissions (
    id         bigserial PRIMARY KEY,
    inviter_id bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    invitee_id bigint REFERENCES users (id) ON DELETE SET NULL,
    order_id   bigint      NOT NULL UNIQUE REFERENCES orders (id) ON DELETE CASCADE,
    amount     bigint      NOT NULL CHECK (amount >= 0),
    status     text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'frozen', 'settled', 'cancelled')),
    reason     text        NOT NULL DEFAULT '',
    release_at timestamptz NOT NULL,
    settled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version    bigint      NOT NULL DEFAULT 1
);
CREATE INDEX commissions_inviter ON commissions (inviter_id, created_at DESC);
CREATE INDEX commissions_due ON commissions (release_at) WHERE status = 'pending';

-- 工单：Agent 只能保存草稿，运营者确认后才发给用户 -----------------------------

CREATE TABLE tickets (
    id                 bigserial PRIMARY KEY,
    user_id            bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject            text        NOT NULL,
    status             text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'replied', 'closed')),
    -- Agent 回答不了（例如开发票），标为「等你处理」
    needs_human        boolean     NOT NULL DEFAULT false,
    needs_human_reason text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    last_message_at    timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz,
    version            bigint      NOT NULL DEFAULT 1
);
CREATE INDEX tickets_user ON tickets (user_id, last_message_at DESC);
CREATE INDEX tickets_status ON tickets (status, last_message_at DESC);

CREATE TABLE ticket_messages (
    id          bigserial PRIMARY KEY,
    ticket_id   bigint      NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    author      text        NOT NULL CHECK (author IN ('user', 'operator')),
    operator_id bigint REFERENCES operators (id) ON DELETE SET NULL,
    body        text        NOT NULL,
    -- 运营者发送的回复如果基于 Agent 草稿，记下草稿由谁草拟
    drafted_by  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_messages_ticket ON ticket_messages (ticket_id, id);

CREATE TABLE ticket_drafts (
    ticket_id  bigint PRIMARY KEY REFERENCES tickets (id) ON DELETE CASCADE,
    body       text        NOT NULL,
    drafted_by text        NOT NULL,
    basis      jsonb       NOT NULL DEFAULT '[]',
    change_id  text        NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Webhook 订阅与事件自动处置 ----------------------------------------------------

CREATE TABLE webhooks (
    id               bigserial PRIMARY KEY,
    name             text        NOT NULL,
    url              text        NOT NULL,
    secret_cipher    bytea       NOT NULL,
    kinds            text[]      NOT NULL DEFAULT '{}',
    enabled          boolean     NOT NULL DEFAULT true,
    last_status      text        NOT NULL DEFAULT '',
    last_delivery_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    version          bigint      NOT NULL DEFAULT 1
);

CREATE TABLE automation_rules (
    id            bigserial PRIMARY KEY,
    name          text        NOT NULL,
    event_kind    text        NOT NULL,
    action        text        NOT NULL CHECK (action IN ('email_user', 'operation', 'replace_offline_node')),
    params        jsonb       NOT NULL DEFAULT '{}',
    enabled       boolean     NOT NULL DEFAULT true,
    last_fired_at timestamptz,
    last_result   text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    version       bigint      NOT NULL DEFAULT 1
);
CREATE INDEX automation_rules_kind ON automation_rules (event_kind) WHERE enabled;

-- 多人审批与一次性码 -------------------------------------------------------------

ALTER TABLE approvals ADD COLUMN required integer NOT NULL DEFAULT 1 CHECK (required >= 1);
ALTER TABLE approvals ADD COLUMN needs_code boolean NOT NULL DEFAULT false;

CREATE TABLE approval_votes (
    approval_id text        NOT NULL REFERENCES approvals (id) ON DELETE CASCADE,
    operator_id bigint      NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    via         text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (approval_id, operator_id)
);

CREATE TABLE approval_codes (
    approval_id text        NOT NULL REFERENCES approvals (id) ON DELETE CASCADE,
    operator_id bigint      NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    code_hash   bytea       NOT NULL,
    attempts    integer     NOT NULL DEFAULT 0,
    expires_at  timestamptz NOT NULL,
    PRIMARY KEY (approval_id, operator_id)
);

-- +goose Down
DROP TABLE approval_codes;
DROP TABLE approval_votes;
ALTER TABLE approvals DROP COLUMN needs_code;
ALTER TABLE approvals DROP COLUMN required;
DROP TABLE automation_rules;
DROP TABLE webhooks;
DROP TABLE ticket_drafts;
DROP TABLE ticket_messages;
DROP TABLE tickets;
DROP TABLE commissions;
DROP TABLE payment_callbacks;
DROP TABLE orders;
DROP TABLE coupons;
DROP TABLE plan_revisions;
ALTER TABLE plans DROP COLUMN rollout_percent;
ALTER TABLE plans DROP COLUMN rollout_prices;
ALTER TABLE nodes DROP COLUMN monthly_cost;
ALTER TABLE users DROP COLUMN register_ip;
ALTER TABLE users DROP COLUMN invited_by;
ALTER TABLE users DROP COLUMN invite_code;
ALTER TABLE users DROP COLUMN balance;
