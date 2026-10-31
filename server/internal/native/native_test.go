package native_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/native"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/testutil"
)

type env struct {
	t     *testing.T
	st    *store.Store
	r     *gin.Engine
	token string
	plan  db.Plan
	node  db.Node
}

func setup(t *testing.T) *env {
	st := testutil.Store(t)
	ctx := context.Background()
	h := native.New(st, native.Config{ReportInterval: 60, SyncInterval: 60, DeviceTTL: 5 * time.Minute}, events.NewRecorder(st, testutil.Logger()), testutil.Logger())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/sync", h.Auth, h.Sync)
	r.POST("/report", h.Auth, h.Report)

	m, err := st.EnsureMachine(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	code := secret.Token("pe")
	exp := time.Now().Add(time.Hour)
	_ = st.SetMachineEnrollment(ctx, db.SetMachineEnrollmentParams{ID: m.ID, EnrollHash: secret.Hash(code), EnrollExpiresAt: &exp})
	token := secret.Token("mt")
	if _, err := st.ClaimMachineEnrollment(ctx, db.ClaimMachineEnrollmentParams{TokenHash: secret.Hash(token), EnrollHash: secret.Hash(code)}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreatePlan(ctx, db.CreatePlanParams{Code: "p", Name: "p", TransferBytes: 1 << 40, ResetMethod: "never", Prices: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.CreateNode(ctx, db.CreateNodeParams{Code: "n1", Name: "东京-01", Host: "m1", Address: "203.0.113.1", Protocol: "shadowsocks",
		Port: 8388, ServerPort: 8388, Settings: []byte(`{"cipher":"2022-blake3-aes-128-gcm","server_key":"MDEyMzQ1Njc4OWFiY2RlZg=="}`),
		Rate: 2, State: "pending", Tags: []string{}, Backend: "native"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AttachNode(ctx, db.AttachNodeParams{PlanID: plan.ID, NodeID: node.ID}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, r: r, token: token, plan: plan, node: node}
	e.addUser(1)
	e.addUser(2)
	return e
}

func (e *env) addUser(i int) db.User {
	u, err := e.st.CreateUser(context.Background(), db.CreateUserParams{Email: fmt.Sprintf("u%d@example.com", i), PasswordAlgo: "argon2id",
		Uuid: fmt.Sprintf("0f7c3a8e-1111-4222-8333-94445555%04d", i), SubToken: fmt.Sprintf("tok-%d", i), PlanID: &e.plan.ID, TransferBytes: 1 << 40})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) post(path, token string, in, out any) int {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			e.t.Fatal(err)
		}
	}
	return w.Code
}

func TestSyncFullThenDelta(t *testing.T) {
	e := setup(t)
	if code := e.post("/sync", "mt_wrong", nodeapi.SyncRequest{}, nil); code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", code)
	}
	var resp nodeapi.SyncResponse
	e.post("/sync", e.token, nodeapi.SyncRequest{}, &resp)
	if len(resp.Nodes) != 1 || resp.Nodes[0].Config == nil || !resp.Nodes[0].FullUsers || len(resp.Nodes[0].Users) != 2 {
		t.Fatalf("first sync: %+v", resp)
	}
	ns := resp.Nodes[0]
	if ns.Config.Protocol != "shadowsocks" || ns.Config.ServerKey == "" || ns.Users[0].Password == ns.Users[0].UUID {
		t.Fatalf("SS2022 users need user keys: %+v", ns)
	}
	known := map[int64]nodeapi.KnownNode{ns.ID: {ConfigRev: ns.ConfigRev, UsersRev: ns.UsersRev}}

	// 没有变化：不重传
	resp = nodeapi.SyncResponse{}
	e.post("/sync", e.token, nodeapi.SyncRequest{Nodes: known}, &resp)
	if len(resp.Nodes) != 0 {
		t.Fatalf("unchanged sync should be empty: %+v", resp.Nodes)
	}

	// 新增一个用户：只发增量
	u3 := e.addUser(3)
	resp = nodeapi.SyncResponse{}
	e.post("/sync", e.token, nodeapi.SyncRequest{Nodes: known}, &resp)
	if len(resp.Nodes) != 1 || resp.Nodes[0].Config != nil || resp.Nodes[0].FullUsers || resp.Nodes[0].Delta == nil {
		t.Fatalf("expected delta: %+v", resp.Nodes)
	}
	d := resp.Nodes[0].Delta
	if len(d.Upsert) != 1 || d.Upsert[0].ID != u3.ID || len(d.Remove) != 0 {
		t.Fatalf("delta: %+v", d)
	}

	// 节点删除后出现在 removed 里
	_ = e.st.DeleteNode(context.Background(), e.node.ID)
	resp = nodeapi.SyncResponse{}
	e.post("/sync", e.token, nodeapi.SyncRequest{Nodes: known}, &resp)
	if len(resp.Removed) != 1 || resp.Removed[0] != e.node.ID {
		t.Fatalf("removed: %+v", resp)
	}
}

func TestReportExactlyOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	users, _ := e.st.AvailableUsersForNode(ctx, e.node.ID)
	report := nodeapi.ReportRequest{Spool: "s1", Seq: 1, At: time.Now(), Nodes: []nodeapi.NodeReport{{
		ID:      e.node.ID,
		Traffic: map[int64][2]int64{users[0].ID: {100, 1000}},
		Alive:   map[int64][]string{users[0].ID: {"198.51.100.7"}},
		Status:  &nodeapi.NodeStatus{Running: true, Users: 2},
	}}}
	var resp nodeapi.ReportResponse
	if code := e.post("/report", e.token, report, &resp); code != 200 || resp.Ack != 1 || resp.Duplicate {
		t.Fatalf("first report: %d %+v", code, resp)
	}
	// 节点没收到确认又重发了同一批
	resp = nodeapi.ReportResponse{}
	if code := e.post("/report", e.token, report, &resp); code != 200 || resp.Ack != 1 || !resp.Duplicate {
		t.Fatalf("duplicate report: %d %+v", code, resp)
	}
	var count int
	if err := e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM traffic_reports WHERE node_id = $1", e.node.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("traffic reports stored: %d %v", count, err)
	}
	// 新队列从 1 开始计数，不会被误判为重复
	report.Spool = "s2"
	resp = nodeapi.ReportResponse{}
	e.post("/report", e.token, report, &resp)
	if resp.Duplicate {
		t.Fatal("new spool must be accepted")
	}
	n, _ := e.st.GetNode(ctx, e.node.ID)
	if n.State != "online" || len(n.Runtime) == 0 {
		t.Fatalf("running status should count as heartbeat: state=%s runtime=%s", n.State, n.Runtime)
	}
	// 内核里没跑起来的节点不算心跳
	other, _ := e.st.CreateNode(ctx, db.CreateNodeParams{Code: "n2", Name: "东京-02", Host: "m1", Address: "203.0.113.1", Protocol: "vless",
		Port: 443, ServerPort: 443, Settings: []byte(`{}`), Rate: 1, State: "pending", Tags: []string{}, Backend: "native"})
	e.post("/report", e.token, nodeapi.ReportRequest{Spool: "s2", Seq: 2, Nodes: []nodeapi.NodeReport{{
		ID: other.ID, Status: &nodeapi.NodeStatus{Running: false, Error: "端口 443 被占用"}}}}, nil)
	if n, _ := e.st.GetNode(ctx, other.ID); n.State != "pending" {
		t.Fatalf("failed node must stay pending, got %s", n.State)
	}
}
