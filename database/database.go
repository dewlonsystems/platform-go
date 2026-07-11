// Package database manages the Postgres connection pool (via pgx/pgxpool)
// and applies SQL migrations.
package database

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dewlonsystems/platform-go/config"
	"github.com/dewlonsystems/platform-go/errors"
	"github.com/dewlonsystems/platform-go/logger"
)

// Connect opens a Postgres connection pool via pgx, applies sane pool
// defaults, and verifies connectivity with a ping before returning.
func Connect(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, errors.NewInternal("failed to parse database connection string", err)
	}

	// Sane pool defaults for a typical web service. Tune to your workload.
	poolCfg.MaxConns = 25
	poolCfg.MinConns = 0
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errors.NewInternal("failed to open database connection", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, errors.NewInternal("failed to connect to database", err)
	}

	return pool, nil
}

// Migrate applies any .sql files in migrationsFS that haven't been applied
// yet, in filename order (so name them 0001_x.sql, 0002_y.sql, ...). Each
// migration runs in its own transaction; applied filenames are tracked in
// a schema_migrations table, so this is safe to call on every startup.
//
// migrationsFS is supplied by the caller — typically the embed.FS exposed
// by the top-level migrations package:
//
//	import "github.com/dewlonsystems/platform-go/migrations"
//	database.Migrate(ctx, pool, migrations.FS)
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrationsFS fs.FS) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    TEXT PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return errors.NewInternal("failed to create schema_migrations table", err)
	}

	applied := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return errors.NewInternal("failed to read applied migrations", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return errors.NewInternal("failed to scan applied migration row", err)
		}
		applied[name] = true
	}
	rows.Close()

	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return errors.NewInternal("failed to read migrations directory", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}

		sqlBytes, err := fs.ReadFile(migrationsFS, path.Join(".", name))
		if err != nil {
			return errors.NewInternal(fmt.Sprintf("failed to read migration %s", name), err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return errors.NewInternal("failed to begin migration transaction", err)
		}

		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			tx.Rollback(ctx)
			return errors.NewInternal(fmt.Sprintf("migration %s failed", name), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return errors.NewInternal(fmt.Sprintf("failed to record migration %s", name), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return errors.NewInternal(fmt.Sprintf("failed to commit migration %s", name), err)
		}

		logger.Log.Info("applied migration", "file", name)
	}

	return nil
}
