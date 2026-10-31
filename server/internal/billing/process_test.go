package billing_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/testutil"
)

// 多个实例同时处理上报时，每条上报只入账一次；节点倍率按上报时的值计算。
func TestConcurrentProcessingCountsOnce(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	ev := events.NewRecorder(st, testutil.Logger())
	plan, err := st.CreatePlan(ctx, db.CreatePlanParams{Code: "p", Name: "p", TransferBytes: 1 << 40, ResetMethod: "never", Prices: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	var users []db.User
	for i := 0; i < 5; i++ {
		u, err := st.CreateUser(ctx, db.CreateUserParams{Email: fmt.Sprintf("c%d@example.com", i), PasswordAlgo: "argon2id",
			Uuid: fmt.Sprintf("uuid-%d", i), SubToken: fmt.Sprintf("tok-%d", i), PlanID: &plan.ID, TransferBytes: 1 << 40})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	const reports = 300
	for i := 0; i < reports; i++ {
		payload := map[string][2]float64{}
		for _, u := range users {
			payload[fmt.Sprint(u.ID)] = [2]float64{100, 200}
		}
		b, _ := json.Marshal(payload)
		if err := st.InsertTrafficReport(ctx, db.InsertTrafficReportParams{NodeID: 1, Rate: 1.5, Payload: b}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := billing.NewProcessor(st, ev, time.UTC, testutil.Logger())
			if _, err := p.ProcessReports(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, u := range users {
		got, _ := st.GetUser(ctx, u.ID)
		if got.UsedUp != reports*150 || got.UsedDown != reports*300 {
			t.Fatalf("user %d: up=%d down=%d", u.ID, got.UsedUp, got.UsedDown)
		}
	}
	total, _ := st.TotalTrafficOnDay(ctx, time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), time.Now().UTC().Day(), 0, 0, 0, 0, time.UTC))
	if total != reports*450*int64(len(users)) {
		t.Fatalf("node daily total: %d", total)
	}
}

func TestResetDue(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	ev := events.NewRecorder(st, testutil.Logger())
	plan, _ := st.CreatePlan(ctx, db.CreatePlanParams{Code: "p", Name: "p", TransferBytes: 100, ResetMethod: "calendar_month", Prices: []byte("{}")})
	past := time.Now().Add(-time.Minute)
	u, err := st.CreateUser(ctx, db.CreateUserParams{Email: "r@example.com", PasswordAlgo: "argon2id", Uuid: "u", SubToken: "t",
		PlanID: &plan.ID, TransferBytes: 100, NextResetAt: &past})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUserTraffic(ctx, db.AddUserTrafficParams{ID: u.ID, Up: 60, Down: 60}); err != nil {
		t.Fatal(err)
	}
	p := billing.NewProcessor(st, ev, time.UTC, testutil.Logger())
	if n, err := p.ResetDue(ctx); err != nil || n != 1 {
		t.Fatalf("reset: %d %v", n, err)
	}
	got, _ := st.GetUser(ctx, u.ID)
	if got.UsedUp+got.UsedDown != 0 || got.NextResetAt == nil || !got.NextResetAt.After(time.Now()) || got.NextResetAt.UTC().Day() != 1 {
		t.Fatalf("after reset: %+v", got)
	}
}
