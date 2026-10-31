// Package xboard 从 Xboard 的数据库导入套餐、节点和用户。
//
// 导入保持原 ID：节点后端的 node_id、流量上报里的用户 ID 都不用改；
// 订阅 token、UUID、密码哈希原样保留，老用户的订阅链接和密码照常可用。
// 只支持 Xboard 2025 年之后的统一节点表（v2_server）。
package xboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type Options struct {
	Driver string // mysql 或 pgx
	DSN    string
	Prefix string // 表前缀，默认 v2_
	DryRun bool
	// DefaultReset 是 Xboard 套餐「跟随系统设置」时使用的重置方式。
	DefaultReset string
	Loc          *time.Location
}

// Report 是导入结果，预演和正式导入都会生成。
type Report struct {
	Plans    int      `json:"plans"`
	Nodes    int      `json:"nodes"`
	Users    int      `json:"users"`
	Attached int      `json:"plan_node_links"`
	Warnings []string `json:"warnings"`
	DryRun   bool     `json:"dry_run"`
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

type row map[string]any

func (r row) str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	case time.Time:
		return strconv.FormatInt(v.Unix(), 10)
	default:
		return fmt.Sprint(v)
	}
}

func (r row) int(k string) int64 {
	s := strings.TrimSpace(r.str(k))
	if s == "" {
		return 0
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f)
}

func (r row) float(k string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(r.str(k)), 64)
	return f
}

func (r row) null(k string) bool { return r[k] == nil }

func (r row) boolean(k string) bool {
	s := strings.ToLower(r.str(k))
	return s == "1" || s == "true"
}

// unix 把 Xboard 的时间字段（整数时间戳或 datetime）换成时间。
func (r row) unix(k string) *time.Time {
	if r.null(k) {
		return nil
	}
	if t, ok := r[k].(time.Time); ok {
		return &t
	}
	n := r.int(k)
	if n <= 0 {
		if t, err := time.Parse("2006-01-02 15:04:05", r.str(k)); err == nil {
			return &t
		}
		return nil
	}
	t := time.Unix(n, 0).UTC()
	return &t
}

func query(ctx context.Context, src *sql.DB, q string) ([]row, error) {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := row{}
		for i, c := range cols {
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Import 读取 Xboard 数据并写入 Patato。目标库里不能已有套餐、节点或用户。
func Import(ctx context.Context, st *store.Store, box *secret.Box, opt Options, log *slog.Logger) (*Report, error) {
	if opt.Prefix == "" {
		opt.Prefix = "v2_"
	}
	if opt.DefaultReset == "" {
		opt.DefaultReset = billing.ResetCalendarMonth
	}
	if opt.Loc == nil {
		opt.Loc = time.UTC
	}
	src, err := sql.Open(opt.Driver, opt.DSN)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect xboard database: %w", err)
	}
	plans, err := query(ctx, src, "SELECT * FROM "+opt.Prefix+"plan ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read plans: %w", err)
	}
	servers, err := query(ctx, src, "SELECT * FROM "+opt.Prefix+"server ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read servers (需要 Xboard 2025 年之后的统一节点表): %w", err)
	}
	users, err := query(ctx, src, "SELECT * FROM "+opt.Prefix+"user ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}

	rep := &Report{DryRun: opt.DryRun}
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	var existing int64
	if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM plans) + (SELECT count(*) FROM nodes) + (SELECT count(*) FROM users)").Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("目标库里已有 %d 条套餐、节点或用户数据，导入只能在空库上进行", existing)
	}

	planGroup := map[int64]int64{}
	for _, p := range plans {
		id := p.int("id")
		planGroup[id] = p.int("group_id")
		method := opt.DefaultReset
		if !p.null("reset_traffic_method") {
			switch p.int("reset_traffic_method") {
			case 0:
				method = billing.ResetCalendarMonth
			case 1:
				method = billing.ResetPurchaseDay
			case 2:
				method = billing.ResetNever
			default:
				method = billing.ResetNever
				rep.warn("套餐 %d（%s）按年重置，Patato 暂不支持，已改为不重置", id, p.str("name"))
			}
		}
		prices := json.RawMessage("{}")
		if s := p.str("prices"); s != "" && json.Valid([]byte(s)) {
			prices = normalizePrices(s)
		}
		_, err := tx.Exec(ctx, `INSERT INTO plans (id, code, name, transfer_bytes, reset_method, device_limit, speed_limit_mbps, prices, sellable)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, fmt.Sprintf("plan-%d", id), p.str("name"), billing.GB(p.float("transfer_enable")), method,
			p.int("device_limit"), p.int("speed_limit"), prices, p.boolean("sell"))
		if err != nil {
			return nil, fmt.Errorf("insert plan %d: %w", id, err)
		}
		rep.Plans++
	}

	for _, s := range servers {
		id := s.int("id")
		typ := proto.Normalize(s.str("type"))
		settings := proto.Parse(json.RawMessage(s.str("protocol_settings")))
		if typ == "shadowsocks" {
			if n := proto.SS2022KeySize(settings.Str("cipher")); n > 0 && settings.Str("server_key") == "" {
				created := s.unix("created_at")
				if !s.null("parent_id") && s.int("parent_id") > 0 {
					for _, p := range servers {
						if p.int("id") == s.int("parent_id") {
							created = p.unix("created_at")
						}
					}
				}
				if created == nil {
					rep.warn("节点 %d 缺少创建时间，无法还原 SS2022 服务端密钥", id)
				} else {
					settings["server_key"] = proto.XboardServerKey(created.Unix(), n)
				}
			}
		}
		port := firstPort(s.str("port"))
		if strings.Contains(s.str("port"), "-") {
			rep.warn("节点 %d 使用端口范围 %s，Patato 订阅里只下发起始端口 %d", id, s.str("port"), port)
		}
		if s.int("parent_id") > 0 {
			rep.warn("节点 %d（%s）是子节点，没有自己的节点后端；接入后不会有心跳，只在没有在线节点时下发", id, s.str("name"))
		}
		var tags []string
		_ = json.Unmarshal([]byte(s.str("tags")), &tags)
		if tags == nil {
			tags = []string{}
		}
		settingsJSON, _ := json.Marshal(settings)
		key := secret.Token("nk")
		_, err := tx.Exec(ctx, `INSERT INTO nodes (id, code, name, host, address, protocol, port, server_port, settings, rate,
				state, tags, sort, key_hash, key_cipher, allow_legacy_token)
			VALUES ($1, $2, $3, '', $4, $5, $6, $7, $8, $9, 'offline', $10, $11, $12, $13, true)`,
			id, fmt.Sprintf("node-%d", id), s.str("name"), s.str("host"), typ, port, s.int("server_port"), settingsJSON,
			nonZero(s.float("rate"), 1), tags, s.int("sort"), secret.Hash(key), box.Seal([]byte(key)))
		if err != nil {
			return nil, fmt.Errorf("insert node %d: %w", id, err)
		}
		rep.Nodes++
		var groups []string
		_ = json.Unmarshal([]byte(s.str("group_ids")), &groups)
		for _, g := range groups {
			gid, _ := strconv.ParseInt(g, 10, 64)
			for pid, pg := range planGroup {
				if pg == gid {
					if _, err := q.AttachNode(ctx, db.AttachNodeParams{PlanID: pid, NodeID: id}); err != nil {
						return nil, err
					}
					rep.Attached++
				}
			}
		}
	}

	groupMismatch := 0
	for _, u := range users {
		id := u.int("id")
		var planID *int64
		if !u.null("plan_id") && u.int("plan_id") > 0 {
			pid := u.int("plan_id")
			if _, ok := planGroup[pid]; ok {
				planID = &pid
				if !u.null("group_id") && u.int("group_id") != planGroup[pid] {
					groupMismatch++
				}
			} else {
				rep.warn("用户 %d 的套餐 %d 不存在，导入后没有套餐", id, pid)
			}
		}
		expires := u.unix("expired_at")
		if expires != nil && expires.Unix() == 0 {
			expires = nil
		}
		next := u.unix("next_reset_at")
		anchor := next
		if anchor == nil {
			anchor = expires
		}
		var deviceLimit, speedLimit *int64
		if !u.null("device_limit") {
			v := u.int("device_limit")
			deviceLimit = &v
		}
		if !u.null("speed_limit") {
			v := u.int("speed_limit")
			speedLimit = &v
		}
		algo := u.str("password_algo")
		if algo == "" {
			algo = "bcrypt"
		}
		_, err := tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, password_algo, password_salt, uuid, sub_token, plan_id,
				banned, transfer_bytes, used_up, used_down, expires_at, device_limit, speed_limit_mbps, reset_anchor,
				next_reset_at, last_reset_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, COALESCE($19, now()))`,
			id, u.str("email"), u.str("password"), algo, u.str("password_salt"), u.str("uuid"), u.str("token"), planID,
			u.boolean("banned"), u.int("transfer_enable"), u.int("u"), u.int("d"), expires, deviceLimit, speedLimit, anchor,
			next, u.unix("last_reset_at"), u.unix("created_at"))
		if err != nil {
			return nil, fmt.Errorf("insert user %d (%s): %w", id, u.str("email"), err)
		}
		rep.Users++
	}
	if groupMismatch > 0 {
		rep.warn("%d 名用户的权限组与其套餐的权限组不一致；Patato 按套餐决定可用节点", groupMismatch)
	}

	for _, t := range []string{"plans", "nodes", "users"} {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s', 'id'), GREATEST((SELECT COALESCE(max(id), 0) FROM %s), 1))", t, t)); err != nil {
			return nil, err
		}
	}
	if err := recordAudit(ctx, q, rep); err != nil {
		return nil, err
	}
	if opt.DryRun {
		log.Info("xboard import dry run finished, rolling back")
		return rep, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return rep, nil
}

func recordAudit(ctx context.Context, q *db.Queries, rep *Report) error {
	detail, _ := json.Marshal(rep)
	return q.InsertAudit(ctx, db.InsertAuditParams{
		SubjectKind: "system", SubjectLabel: "patato-server import-xboard", Entry: "system", Action: "xboard.import",
		Result: "success", Detail: detail,
	})
}

func firstPort(s string) int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "-,"); i > 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
}

func nonZero(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

// normalizePrices 把 Xboard 的价格（可能是数字或字符串）统一成整数，单位保持原样（分）。
func normalizePrices(s string) json.RawMessage {
	var raw map[string]any
	if json.Unmarshal([]byte(s), &raw) != nil {
		return json.RawMessage("{}")
	}
	out := map[string]int64{}
	for k, v := range raw {
		switch n := v.(type) {
		case float64:
			out[k] = int64(n)
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				out[k] = int64(f)
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}
