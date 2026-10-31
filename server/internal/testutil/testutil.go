// Package testutil 为集成测试提供真实的 PostgreSQL：每个测试一个独立的空库。
//
// 设置了 PATATO_TEST_DATABASE_URL 时连这个实例（CI 用服务容器）；
// 否则用 testcontainers 临时起一个 postgres:16 容器（需要本机有 Docker）。
package testutil

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/momaek/patato/server/internal/jobs"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
)

var (
	once    sync.Once
	baseURL string
	baseErr error
)

func base(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		if u := os.Getenv("PATATO_TEST_DATABASE_URL"); u != "" {
			baseURL = u
			return
		}
		ctx := context.Background()
		c, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("patato"), postgres.WithUsername("patato"), postgres.WithPassword("patato"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
		if err != nil {
			baseErr = err
			return
		}
		baseURL, baseErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if baseErr != nil {
		t.Skipf("no test database: %v (set PATATO_TEST_DATABASE_URL or run Docker)", baseErr)
	}
	return baseURL
}

// NewDatabase 创建一个空库并返回它的连接串。
func NewDatabase(t *testing.T) string {
	t.Helper()
	b := base(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, b)
	if err != nil {
		t.Fatalf("connect test postgres: %v", err)
	}
	defer conn.Close(ctx)
	name := "t_" + strings.ReplaceAll(secret.ID("db"), "db_", "")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), b)
		if err == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(context.Background())
		}
	})
	u, err := url.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Store 返回一个已迁移到最新结构的独立数据库。
func Store(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Migrate(ctx, st.Pool); err != nil {
		t.Fatal(err)
	}
	return st
}

func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Must 在出错时让测试失败。
func Must[T any](t *testing.T) func(v T, err error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}
