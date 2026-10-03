package bluememo

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/yeomyeonggeori/bluememo/migrations"
)

// OpenToRead opens a store another process keeps, for a reader who may read
// the file and nothing more. Nothing is created, migrated or reinforced, so a
// recall leaves the file as it found it; the keeper reinforces what was
// recalled with Reinforce.
//
// A reader that cannot write a WAL database needs its -wal and -shm files to
// exist already (https://sqlite.org/wal.html#readonly), which they do while
// the keeper holds the store open.
func OpenToRead(ctx context.Context, path string, configuration Configuration) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrNoPath
	}
	if _, errorValue := os.Stat(path); errorValue != nil {
		return nil, fmt.Errorf("open memory database %s to read: %w", path, errorValue)
	}
	database, errorValue := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if errorValue != nil {
		return nil, errorValue
	}
	if errorValue := requireCurrentSchema(ctx, database); errorValue != nil {
		database.Close()
		return nil, fmt.Errorf("open memory database %s to read: %w", path, errorValue)
	}
	return &Store{database: database, configuration: withDefaults(configuration), isReadOnly: true}, nil
}

func requireCurrentSchema(ctx context.Context, database *sql.DB) error {
	names, errorValue := fs.Glob(migrations.Files, "*.sql")
	if errorValue != nil {
		return errorValue
	}
	var appliedCount int
	if errorValue := database.QueryRowContext(ctx, `pragma user_version`).Scan(&appliedCount); errorValue != nil {
		return errorValue
	}
	if appliedCount != len(names) {
		return fmt.Errorf("the file is at schema %d and this reader reads schema %d; its keeper opens it to migrate it", appliedCount, len(names))
	}
	return nil
}

// Reinforce strengthens the memories a reader recalled, for the keeper of a
// store that reader opened with OpenToRead.
func (store *Store) Reinforce(ctx context.Context, memoryIDs []string) error {
	if len(memoryIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(memoryIDs)), ", ")
	arguments := make([]any, len(memoryIDs))
	for index, memoryID := range memoryIDs {
		arguments[index] = memoryID
	}
	memories, errorValue := queryMemories(ctx, store.database, `where cold_since is null and memory_id in (`+placeholders+`)`, arguments...)
	if errorValue != nil {
		return errorValue
	}
	recalled := make([]RecalledMemory, len(memories))
	for index, memory := range memories {
		recalled[index] = RecalledMemory{Memory: memory}
	}
	return store.reinforce(ctx, recalled, store.now())
}
