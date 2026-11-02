package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies the "-- +goose Up" section of every pending migration.
// It records versions in goose_db_version, the table the goose CLI uses,
// so you can switch to `goose up/down/status` at any time.
func Migrate(ctx context.Context, pool *pgxpool.Pool, files fs.FS) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS goose_db_version (
			id SERIAL PRIMARY KEY,
			version_id BIGINT NOT NULL,
			is_applied BOOLEAN NOT NULL,
			tstamp TIMESTAMP DEFAULT now()
		)`); err != nil {
		return err
	}
	// Serialise concurrent migrators (several API replicas starting at once).
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_lock(726317)`); err != nil {
		return err
	}
	defer pool.Exec(context.Background(), `SELECT pg_advisory_unlock(726317)`)

	var current int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&current); err != nil {
		return err
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", name)
		}
		if version <= current {
			continue
		}
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		up := upSection(string(raw))
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, up); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, version)
			return err
		})
		if err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		slog.Info("migration applied", "file", name)
	}
	return nil
}

func upSection(sql string) string {
	start := strings.Index(sql, "-- +goose Up")
	if start >= 0 {
		sql = sql[start+len("-- +goose Up"):]
	}
	if end := strings.Index(sql, "-- +goose Down"); end >= 0 {
		sql = sql[:end]
	}
	return sql
}
