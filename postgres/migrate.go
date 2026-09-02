package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yeomyeonggeori/bluememo/migrations"
)

func ApplyMigrations(ctx context.Context, database *sql.DB) error {
	if _, errorValue := database.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS memory_schema_migration (
  file_name text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); errorValue != nil {
		return errorValue
	}
	list, errorValue := migrations.List()
	if errorValue != nil {
		return errorValue
	}
	for _, migration := range list {
		var isApplied bool
		if errorValue := database.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_schema_migration WHERE file_name = $1)`, migration.Name).Scan(&isApplied); errorValue != nil {
			return errorValue
		}
		if isApplied {
			continue
		}
		if _, errorValue := database.ExecContext(ctx, migration.SQL); errorValue != nil {
			return fmt.Errorf("apply memory migration %s: %w", migration.Name, errorValue)
		}
		if _, errorValue := database.ExecContext(ctx, `INSERT INTO memory_schema_migration (file_name) VALUES ($1) ON CONFLICT DO NOTHING`, migration.Name); errorValue != nil {
			return errorValue
		}
	}
	return nil
}
