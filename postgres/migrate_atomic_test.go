package postgres_test

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/migrations"
	"github.com/yeomyeonggeori/bluememo/postgres"
)

func openMigrationDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	connectionString := os.Getenv("BLUEMEMO_TEST_POSTGRES_URL")
	if connectionString == "" {
		t.Skip("set BLUEMEMO_TEST_POSTGRES_URL to run the postgres checks")
	}
	ctx := context.Background()
	adminDatabase, errorValue := sql.Open("pgx", connectionString)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	schemaName := "bluememo_migration_test_" + bluememo.NewIdentifier()
	if _, errorValue := adminDatabase.ExecContext(ctx, "CREATE SCHEMA "+schemaName); errorValue != nil {
		_ = adminDatabase.Close()
		t.Fatal(errorValue)
	}
	configuration, errorValue := pgx.ParseConfig(connectionString)
	if errorValue != nil {
		_, _ = adminDatabase.ExecContext(ctx, "DROP SCHEMA "+schemaName+" CASCADE")
		_ = adminDatabase.Close()
		t.Fatal(errorValue)
	}
	configuration.RuntimeParams["search_path"] = schemaName + ",public"
	database := stdlib.OpenDB(*configuration)
	database.SetMaxOpenConns(4)
	t.Cleanup(func() {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA "+schemaName+" CASCADE")
		_ = adminDatabase.Close()
	})
	return database, schemaName
}

func TestApplyMigrationsSerializesConcurrentCalls(t *testing.T) {
	database, _ := openMigrationDatabase(t)
	const callerCount = 8
	start := make(chan struct{})
	errorValues := make(chan error, callerCount)
	var waitGroup sync.WaitGroup
	for range callerCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			errorValues <- postgres.ApplyMigrations(context.Background(), database)
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorValues)
	for errorValue := range errorValues {
		if errorValue != nil {
			t.Fatalf("expected concurrent migrations to succeed: %v", errorValue)
		}
	}
	migrationList, errorValue := migrations.List()
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	var appliedCount int
	if errorValue := database.QueryRow(`SELECT count(*) FROM memory_schema_migration`).Scan(&appliedCount); errorValue != nil {
		t.Fatal(errorValue)
	}
	if appliedCount != len(migrationList) {
		t.Fatalf("expected each migration to be recorded once, got %d rows for %d migrations", appliedCount, len(migrationList))
	}
}

func TestApplyMigrationsRollsBackWhenLedgerInsertFails(t *testing.T) {
	database, schemaName := openMigrationDatabase(t)
	_, errorValue := database.Exec(`
CREATE TABLE memory_schema_migration (
  file_name text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION reject_memory_migration_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'migration ledger insert rejected';
END;
$$;
CREATE TRIGGER reject_memory_migration_insert
BEFORE INSERT ON memory_schema_migration
FOR EACH ROW EXECUTE FUNCTION reject_memory_migration_insert()`)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if errorValue := postgres.ApplyMigrations(context.Background(), database); errorValue == nil {
		t.Fatal("expected the migration ledger insert to fail")
	}
	var isMigrationTablePresent bool
	if errorValue := database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, schemaName+`.memory_episode`).Scan(&isMigrationTablePresent); errorValue != nil {
		t.Fatal(errorValue)
	}
	if isMigrationTablePresent {
		t.Fatal("expected migration schema changes to roll back with the failed ledger insert")
	}
	var appliedCount int
	if errorValue := database.QueryRow(`SELECT count(*) FROM memory_schema_migration`).Scan(&appliedCount); errorValue != nil {
		t.Fatal(errorValue)
	}
	if appliedCount != 0 {
		t.Fatalf("expected no migration ledger entries after rollback, got %d", appliedCount)
	}
}
