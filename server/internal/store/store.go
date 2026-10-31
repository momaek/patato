// Package store 管理 PostgreSQL 连接、结构迁移和事务。查询代码由 sqlc 生成在 db 子包。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/momaek/patato/server/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	Pool *pgxpool.Pool
	*db.Queries
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{Pool: pool, Queries: db.New(pool)}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Migrate 把数据库结构升级到最新版本。
func (s *Store) Migrate(ctx context.Context) error {
	sqlDB := stdlib.OpenDBFromPool(s.Pool)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Tx 在一个事务里执行 fn；fn 返回错误或 panic 时回滚。
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries, tx pgx.Tx) error) (err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(s.Queries.WithTx(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IsNotFound 判断查询是否没有返回行。
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation 判断是否违反唯一约束。
func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
