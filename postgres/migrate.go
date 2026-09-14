package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yeomyeonggeori/bluememo/migrations"
)

const memoryMigrationLockID int64 = 0x424c55454d454d4f

func ApplyMigrations(ctx context.Context, database *sql.DB) error {
	list, errorValue := migrations.List()
	if errorValue != nil {
		return errorValue
	}
	transaction, errorValue := database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	if _, errorValue := transaction.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, memoryMigrationLockID); errorValue != nil {
		return errorValue
	}
	if _, errorValue := transaction.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS memory_schema_migration (
  file_name text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); errorValue != nil {
		return errorValue
	}
	for _, migration := range list {
		var isApplied bool
		if errorValue := transaction.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_schema_migration WHERE file_name = $1)`, migration.Name).Scan(&isApplied); errorValue != nil {
			return errorValue
		}
		if isApplied {
			continue
		}
		if _, errorValue := transaction.ExecContext(ctx, migration.SQL); errorValue != nil {
			return fmt.Errorf("apply memory migration %s: %w", migration.Name, errorValue)
		}
		if _, errorValue := transaction.ExecContext(ctx, `INSERT INTO memory_schema_migration (file_name) VALUES ($1) ON CONFLICT DO NOTHING`, migration.Name); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}
