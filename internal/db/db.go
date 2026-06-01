// Package db owns the PostgreSQL connection pool and a tiny schema bootstrap.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgx connection pool and verifies it with a ping, so the
// process fails fast at startup if the database is unreachable.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// schemaDDL creates the listings table if it is not already there.
//
// Money (price, trade_in_value) is BIGINT, holding whole rupiah, never a float:
// floating point cannot represent money exactly and rounding drift on currency
// is a real bug. The CHECK constraints mirror the validation in the Go layer,
// so the database is the last line of defence even if a bug bypasses the service.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS listings (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title          TEXT        NOT NULL,
    description    TEXT        NOT NULL DEFAULT '',
    category       TEXT        NOT NULL DEFAULT '',
    condition      TEXT        NOT NULL CHECK (condition IN ('new','like_new','good','fair')),
    price          BIGINT      NOT NULL CHECK (price >= 0),
    trade_in_value BIGINT      NOT NULL DEFAULT 0 CHECK (trade_in_value >= 0),
    status         TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','sold')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// EnsureSchema runs the idempotent DDL above. This is a pragmatic bootstrap, not
// a migration tool: production would use a versioned migration runner
// (golang-migrate, goose) with up/down files. Keeping it inline makes the demo
// self-contained and runnable with a single `docker compose up`.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaDDL); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}
