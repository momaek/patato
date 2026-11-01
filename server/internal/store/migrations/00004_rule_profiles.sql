-- +goose Up

-- 分流规则：subconverter 格式的 ini 模板（ACL4SSR 等），决定订阅里的策略组和分流规则。
-- 套餐可以指定默认规则，用户可以从允许自选的规则里换一个；都没有时用内置的简单规则。

CREATE TABLE rule_profiles (
    id              bigserial PRIMARY KEY,
    code            text        NOT NULL UNIQUE,
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    -- source_url 非空时定期从这个地址拉取 ini；为空时 content 是管理员直接粘贴的内容
    source_url      text        NOT NULL DEFAULT '',
    content         text        NOT NULL DEFAULT '',
    user_selectable boolean     NOT NULL DEFAULT true,
    fetched_at      timestamptz,
    fetch_error     text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    version         bigint      NOT NULL DEFAULT 1
);

-- 规则文件（.list）缓存。客户端从订阅域名拉取，避免直连 GitHub；拉取失败时继续下发上一次的内容。
CREATE TABLE rule_lists (
    key         text PRIMARY KEY,
    url         text        NOT NULL UNIQUE,
    content     text        NOT NULL DEFAULT '',
    fetched_at  timestamptz,
    fetch_error text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE plans ADD COLUMN rule_profile text REFERENCES rule_profiles (code) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN rule_profile text REFERENCES rule_profiles (code) ON DELETE SET NULL;
CREATE INDEX users_rule_profile ON users (rule_profile) WHERE rule_profile IS NOT NULL;

-- +goose Down
DROP INDEX users_rule_profile;
ALTER TABLE users DROP COLUMN rule_profile;
ALTER TABLE plans DROP COLUMN rule_profile;
DROP TABLE rule_lists;
DROP TABLE rule_profiles;
