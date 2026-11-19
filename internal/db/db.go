// Package db abre o pool de conexões e aplica as migrations embutidas no binário.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thallesdaniell/sre-agent/migrations"
)

// Open cria o pool. O intake fica com poucas conexões; o job abre, usa e fecha.
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL não definida")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("banco inacessível: %w", err)
	}
	return pool, nil
}

const lockID = 74_210_001 // advisory lock: duas réplicas subindo juntas não migram em paralelo

// Migrate aplica, em ordem, os .sql que ainda não constam em schema_migrations. Cada arquivo roda
// numa transação; se falhar, nada dele fica. Migration aplicada nunca é editada: mudança nova = arquivo novo.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return MigrateFS(ctx, pool, migrations.FS)
}

func MigrateFS(ctx context.Context, pool *pgxpool.Pool, files fs.FS) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, err
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	rows.Close()

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var done []string
	for _, name := range names {
		if applied[name] {
			continue
		}
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return done, err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return done, err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return done, fmt.Errorf("migration %s falhou: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return done, err
		}
		if err := tx.Commit(ctx); err != nil {
			return done, err
		}
		done = append(done, name)
	}
	return done, nil
}

// Describe formata o resultado do migrate para o log.
func Describe(applied []string) string {
	if len(applied) == 0 {
		return "nada a aplicar"
	}
	return "aplicadas: " + strings.Join(applied, ", ")
}
