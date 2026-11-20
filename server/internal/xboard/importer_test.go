package xboard

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/subscribe"
	"github.com/momaek/patato/server/internal/testutil"
)

// fakeXboard 在 PostgreSQL 里建一个最小的 Xboard 库（字段与 Xboard 一致的子集）。
func fakeXboard(t *testing.T) string {
	dsn := testutil.NewDatabase(t)
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	md5pw := md5.Sum([]byte("old-password"))
	stmts := []string{
		`CREATE TABLE v2_plan (id int, group_id int, name text, transfer_enable int, speed_limit int, device_limit int,
			reset_traffic_method int, prices text, sell boolean)`,
		`CREATE TABLE v2_server (id int, type text, parent_id int, group_ids text, name text, rate numeric, tags text, host text,
			port text, server_port int, protocol_settings text, show boolean, sort int, created_at timestamp)`,
		`CREATE TABLE v2_user (id int, email text, password text, password_algo text, password_salt text, uuid text, token text,
			plan_id int, group_id int, banned int, transfer_enable bigint, u bigint, d bigint, expired_at bigint, speed_limit int,
			device_limit int, next_reset_at int, last_reset_at int, created_at int)`,
		`INSERT INTO v2_plan VALUES (3, 7, '高级', 300, 0, 3, 1, '{"monthly": 19.9, "quarterly": "54.90", "onetime": null}', true),
			(4, 8, '年付', 1000, NULL, NULL, 4, NULL, false)`,
		`INSERT INTO v2_server VALUES
			(10, 'vless', NULL, '["7"]', '东京-01', 1.5, '["jp"]', 'jp.example.com', '443', 443,
			 '{"tls":2,"network":"tcp","reality_settings":{"public_key":"PUB","private_key":"PRIV","short_id":"ab","server_name":"www.apple.com"}}', true, 1, '2025-01-01 00:00:00'),
			(11, 'shadowsocks', NULL, '["7","8"]', '香港-01', 1, NULL, 'hk.example.com', '20000-20010', 8388,
			 '{"cipher":"2022-blake3-aes-128-gcm"}', true, 2, '2025-01-01 00:00:00'),
			(12, 'shadowsocks', NULL, '["7"]', '洛杉矶-01', 1, NULL, 'la.example.com', '8388', 8388,
			 '{"cipher":"aes-128-gcm"}', false, 3, '2025-01-01 00:00:00'),
			(13, 'shadowsocks', 12, '["7"]', '洛杉矶 广州中转', 1, NULL, 'gz.example.com', '60001', 60001,
			 '{"cipher":"aes-128-gcm"}', true, 4, '2025-01-01 00:00:00'),
			(14, 'shadowsocks', 12, '["7"]', '洛杉矶 广州中转 2', 1, NULL, 'gz.example.com', '60002', 60002,
			 '{"cipher":"aes-128-gcm"}', true, 5, '2025-01-01 00:00:00')`,
		`INSERT INTO v2_user VALUES
			(100, 'old@example.com', '` + hex.EncodeToString(md5pw[:]) + `', 'md5', '', '0f7c3a8e-1111-4222-8333-944455556666',
			 'abcdef0123456789abcdef0123456789', 3, 7, 0, 322122547200, 100, 200, 1893456000, NULL, NULL, NULL, NULL, 1700000000),
			(101, 'expired@example.com', '$2y$10$abcdefghijklmnopqrstuv', NULL, NULL, '1f7c3a8e-1111-4222-8333-944455556666',
			 'ffffef0123456789abcdef0123456789', 99, 7, 0, 0, 0, 0, NULL, NULL, 1, NULL, NULL, 1700000000)`,
		`ALTER TABLE v2_user ADD COLUMN invite_user_id int, ADD COLUMN balance int DEFAULT 0, ADD COLUMN commission_balance int DEFAULT 0`,
		`UPDATE v2_user SET invite_user_id = 100 WHERE id = 101`,
		`UPDATE v2_user SET balance = 500, commission_balance = 120 WHERE id = 100`,
		`CREATE TABLE v2_order (id int, user_id int, plan_id int, type int, period text, trade_no text, total_amount int,
			discount_amount int, balance_amount int, status int, created_at int, updated_at int, paid_at int)`,
		`INSERT INTO v2_order VALUES (1, 100, 3, 2, 'month_price', 'XB0001', 1790, 200, 0, 3, 1700000000, 1700000100, 1700000100),
			(2, 100, 3, 2, 'monthly', 'XB0002', 1990, 0, 0, 0, 1700000000, 1700000000, NULL)`,
	}
	for _, s := range stmts {
		if _, err := c.Exec(ctx, s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return dsn
}

func TestImport(t *testing.T) {
	src := fakeXboard(t)
	st := testutil.Store(t)
	box, _ := secret.NewBox(strings.Repeat("k", 40))
	ctx := context.Background()

	rep, err := Import(ctx, st, box, Options{Driver: "pgx", DSN: src, DryRun: true}, testutil.Logger())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Plans != 2 || rep.Nodes != 3 || rep.Users != 2 || rep.Entries != 2 {
		t.Fatalf("dry run report: %+v", rep)
	}
	if n, _ := st.ListNodes(ctx); len(n) != 0 {
		t.Fatal("dry run must not write")
	}

	rep, err = Import(ctx, st, box, Options{Driver: "pgx", DSN: src}, testutil.Logger())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Attached != 4 {
		t.Fatalf("plan-node links: %+v", rep)
	}
	// 子节点导入成父节点的入口；父节点隐藏，直连入口停用
	if _, err := st.GetNode(ctx, 13); err == nil {
		t.Fatal("child node must not be imported as a node")
	}
	es, _ := st.ListNodeEntries(ctx, 12)
	if len(es) != 3 || es[0].Enabled || es[0].Address != "" || es[1].Name != "洛杉矶 广州中转" || es[1].Address != "gz.example.com" ||
		es[1].Port != 60001 || !es[1].Enabled || es[2].Port != 60002 {
		t.Fatalf("entries: %+v", es)
	}
	if es10, _ := st.ListNodeEntries(ctx, 10); len(es10) != 0 {
		t.Fatalf("visible node without children keeps the implicit direct entry: %+v", es10)
	}
	// 订阅与 Xboard 一致：显示的节点（东京、香港、两个子节点）按名称、地址、端口逐条对应，隐藏的父节点不出现
	owner, _ := st.GetUser(ctx, 100)
	servers, err := subscribe.ServersFor(ctx, st.Queries, owner)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, sv := range servers {
		got = append(got, fmt.Sprintf("%s %s:%d", sv.Name, sv.Host, sv.Port))
	}
	want := "东京-01 jp.example.com:443|香港-01 hk.example.com:20000|洛杉矶 广州中转 gz.example.com:60001|洛杉矶 广州中转 2 gz.example.com:60002"
	if strings.Join(got, "|") != want {
		t.Fatalf("subscription after import:\n got  %s\n want %s", strings.Join(got, "|"), want)
	}
	// 只导入已完成的订单；邀请关系和余额（余额 + 佣金余额）一起迁移
	if rep.Orders != 1 || rep.Invites != 1 || rep.Balance != 620 {
		t.Fatalf("orders / invites / balance: %+v", rep)
	}
	o, err := st.GetOrderByTradeNo(ctx, "XB0001")
	if err != nil || o.Status != "paid" || o.Period != "monthly" || o.Kind != "renew" || o.Total != 1790 || o.Amount != 1990 || o.Source != "xboard" {
		t.Fatalf("imported order: %+v %v", o, err)
	}
	invitee, _ := st.GetUser(ctx, 101)
	if invitee.InvitedBy == nil || *invitee.InvitedBy != 100 {
		t.Fatalf("invite link: %+v", invitee)
	}
	joined := strings.Join(rep.Warnings, "\n")
	for _, want := range []string{"按年重置", "端口范围", "套餐 99 不存在"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning %q in:\n%s", want, joined)
		}
	}

	// 原 ID、订阅 token、UUID 保持不变
	u, err := st.GetUserBySubToken(ctx, "abcdef0123456789abcdef0123456789")
	if err != nil || u.ID != 100 || u.Uuid != "0f7c3a8e-1111-4222-8333-944455556666" || u.UsedUp != 100 {
		t.Fatalf("user: %+v %v", u, err)
	}
	if u.ExpiresAt == nil || !u.ExpiresAt.Equal(time.Unix(1893456000, 0)) {
		t.Fatalf("expires: %v", u.ExpiresAt)
	}
	pl, _ := st.GetPlan(ctx, 3)
	if pl.ResetMethod != "purchase_day" || pl.TransferBytes != 300<<30 || string(pl.Prices) != `{"monthly": 1990, "quarterly": 5490}` {
		t.Fatalf("plan: %+v %s", pl, pl.Prices)
	}

	// SS2022 服务端密钥按 Xboard 的算法从创建时间还原
	ss, _ := st.GetNode(ctx, 11)
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	if got := proto.Parse(ss.Settings).Str("server_key"); got != proto.XboardServerKey(created, 16) {
		t.Fatalf("server key: %s", got)
	}
	if ss.Port != 20000 || !ss.AllowLegacyToken || ss.State != "offline" {
		t.Fatalf("ss node: %+v", ss)
	}
	vl, _ := st.GetNode(ctx, 10)
	if vl.Rate != 1.5 || proto.Parse(vl.Settings).Str("reality_settings.private_key") != "PRIV" {
		t.Fatalf("vless node: %+v", vl)
	}

	// 新建的记录 ID 接在导入数据后面
	n, err := st.CreatePlan(ctx, dbPlan("next"))
	if err != nil || n.ID != 5 {
		t.Fatalf("sequence not advanced: %d %v", n.ID, err)
	}

	// 老密码（md5）可以直接登录，登录后升级为 argon2id
	cfg := config.Default()
	cfg.SecretKey = strings.Repeat("k", 40)
	svc, _ := auth.New(st, cfg, testutil.Logger())
	if _, err := svc.UserLogin(ctx, "old@example.com", "old-password"); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, 100)
	if u.PasswordAlgo != "argon2id" {
		t.Fatalf("password should be upgraded, got %s", u.PasswordAlgo)
	}
	if _, err := svc.UserLogin(ctx, "old@example.com", "old-password"); err != nil {
		t.Fatal("upgraded hash must still verify")
	}

	// 目标库已有数据时拒绝再次导入
	if _, err := Import(ctx, st, box, Options{Driver: "pgx", DSN: src}, testutil.Logger()); err == nil {
		t.Fatal("import into non-empty database must fail")
	}
}

func dbPlan(code string) db.CreatePlanParams {
	return db.CreatePlanParams{Code: code, Name: code, TransferBytes: 1, ResetMethod: "never", Prices: []byte("{}")}
}

// Xboard 的价格以元为单位、带小数；导入后按分存储，浮点误差要四舍五入掉。
func TestNormalizePrices(t *testing.T) {
	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	got := string(normalizePrices(`{"monthly": 19.9, "quarterly": 0.29, "yearly": "199.99", "onetime": 10, "reset_traffic": null, "bad": "abc", "neg": -1}`, warn))
	want := `{"monthly":1990,"onetime":1000,"quarterly":29,"yearly":19999}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if len(warnings) != 2 {
		t.Fatalf("want 2 warnings for the invalid prices, got %v", warnings)
	}
}
