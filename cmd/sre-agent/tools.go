package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thallesdaniell/sre-agent/internal/db"
	"github.com/thallesdaniell/sre-agent/internal/postmortem"
	"github.com/thallesdaniell/sre-agent/internal/review"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

// runMigrate aplica as migrations embutidas. No Kubernetes roda como initContainer do intake.
func runMigrate([]string) error {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Println(db.Describe(applied))
	return nil
}

func runReview(args []string) error {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	st := store.NewPG(pool)
	defer st.Close()
	out, err := review.Run(ctx, st, args, os.Getenv("TRACE_URL_TEMPLATE"), false)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runImportPostmortems(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("uso: sre-agent import-postmortems <pasta>")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	st := store.NewPG(pool)
	defer st.Close()
	files, err := filepath.Glob(filepath.Join(args[0], "*.md"))
	if err != nil {
		return err
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		pm, err := postmortem.Parse(file, string(raw))
		if err != nil {
			return err
		}
		if err := st.UpsertPostmortem(ctx, pm); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		fmt.Println("importado:", pm.Slug)
	}
	fmt.Printf("%d post-mortem(s)\n", len(files))
	return nil
}
