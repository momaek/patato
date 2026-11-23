-- +goose Up

-- 入口组：线路的第一跳可以由多台机器共同承担，它们共用一个对外域名（entry_address），
-- 域名解析到哪台都能连上。设计见 docs/relay.md「入口组」。

-- entry_peers：与第一跳（hops[1]）一起承担入口的其他机器，machines.id 列表，不能与 hops 重叠。
-- entry_address：线路给客户端的入口地址；非空时订阅里经这条线路的入口用它（入口自己覆盖的地址优先）。
--   有 entry_peers 时必须填写，否则客户端只能连到第一跳那一台。
-- entry_ips：服务端最近一次解析 entry_address 得到的地址，用来判断域名当前指向入口组里的哪几台。
ALTER TABLE routes
    ADD COLUMN entry_peers       bigint[]    NOT NULL DEFAULT '{}',
    ADD COLUMN entry_address     text        NOT NULL DEFAULT '',
    ADD COLUMN entry_ips         text[]      NOT NULL DEFAULT '{}',
    ADD COLUMN entry_resolved_at timestamptz,
    ADD CONSTRAINT routes_peers_need_address CHECK (cardinality(entry_peers) = 0 OR entry_address <> '');

-- mirror：入口组成员上的镜像转发。与同一入口第一跳的转发监听同一个端口、转到同一个下一跳，
-- 只有第一跳（hop = 0）有镜像，每个入口在每台成员机器上最多一条。
ALTER TABLE forwards
    ADD COLUMN mirror boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT forwards_mirror_first_hop CHECK (NOT mirror OR hop = 0),
    DROP CONSTRAINT forwards_entry_id_hop_key;
CREATE UNIQUE INDEX forwards_entry_hop ON forwards (entry_id, hop) WHERE NOT mirror;
CREATE UNIQUE INDEX forwards_entry_mirror ON forwards (entry_id, machine_id) WHERE mirror;

-- +goose Down
DROP INDEX forwards_entry_mirror;
DROP INDEX forwards_entry_hop;
DELETE FROM forwards WHERE mirror;
ALTER TABLE forwards
    DROP CONSTRAINT forwards_mirror_first_hop,
    DROP COLUMN mirror,
    ADD CONSTRAINT forwards_entry_id_hop_key UNIQUE (entry_id, hop);
ALTER TABLE routes
    DROP CONSTRAINT routes_peers_need_address,
    DROP COLUMN entry_resolved_at,
    DROP COLUMN entry_ips,
    DROP COLUMN entry_address,
    DROP COLUMN entry_peers;
