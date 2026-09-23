package bluememo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yeomyeonggeori/bluememo/migrations"
	_ "modernc.org/sqlite"
)

const (
	DefaultCapacity      = 10000
	DefaultStaticShare   = 0.2
	DefaultColdGrace     = 30 * 24 * time.Hour
	DefaultClaimDuration = 10 * time.Minute
	databaseFileMode     = 0o600
)

type Configuration struct {
	Embedder          Embedder
	EmbeddingModel    string
	Model             LanguageModel
	Judge             Judge
	People            EntityResolver
	Capacity          int
	StaticShare       float64
	TombstoneCapacity int
	ColdGrace         time.Duration
	HalfLife          time.Duration
	ClaimDuration     time.Duration
	Location          *time.Location
	Logger            *slog.Logger
	Now               func() time.Time
}

type Store struct {
	database      *sql.DB
	configuration Configuration
}

type Note struct {
	GroupID     string
	Body        string
	SpeakerName string
	IsExplicit  bool
}

var ErrEmptyNote = errors.New("a note needs a body")

func Open(ctx context.Context, path string, configuration Configuration) (*Store, error) {
	if errorValue := createPrivateFile(path); errorValue != nil {
		return nil, errorValue
	}
	database, errorValue := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if errorValue != nil {
		return nil, errorValue
	}
	if errorValue := applyMigrations(ctx, database); errorValue != nil {
		database.Close()
		return nil, errorValue
	}
	return &Store{database: database, configuration: withDefaults(configuration)}, nil
}

func (store *Store) Close() error {
	return store.database.Close()
}

func (store *Store) Memorize(ctx context.Context, note Note) error {
	body := strings.TrimSpace(note.Body)
	if body == "" {
		return ErrEmptyNote
	}
	groupID := strings.TrimSpace(note.GroupID)
	if groupID == "" {
		groupID = NewIdentifier()
	}
	_, errorValue := store.database.ExecContext(ctx,
		`insert into pending_note (note_id, group_id, body, speaker_name, is_explicit, arrived_at) values (?, ?, ?, ?, ?, ?)`,
		NewIdentifier(), groupID, body, strings.TrimSpace(note.SpeakerName), note.IsExplicit, toMilliseconds(store.now()))
	return errorValue
}

func (store *Store) Memories(ctx context.Context) ([]Memory, error) {
	return queryMemories(ctx, store.database, `where cold_since is null order by created_at desc`)
}

func (store *Store) Tombstones(ctx context.Context) ([]Tombstone, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select memory_id, content, is_static, occurred_at, origin_id, reason, request_phrase, created_at, died_at
		from tombstone order by died_at desc`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	tombstones := []Tombstone{}
	for rows.Next() {
		var tombstone Tombstone
		var occurredAt sql.NullInt64
		var createdAt, diedAt int64
		if errorValue := rows.Scan(&tombstone.MemoryID, &tombstone.Content, &tombstone.IsStatic, &occurredAt, &tombstone.OriginID,
			&tombstone.Reason, &tombstone.RequestPhrase, &createdAt, &diedAt); errorValue != nil {
			return nil, errorValue
		}
		tombstone.OccurredAt = fromNullMilliseconds(occurredAt)
		tombstone.CreatedAt = fromMilliseconds(createdAt)
		tombstone.DiedAt = fromMilliseconds(diedAt)
		tombstones = append(tombstones, tombstone)
	}
	return tombstones, rows.Err()
}

func createPrivateFile(path string) error {
	file, errorValue := os.OpenFile(path, os.O_CREATE|os.O_RDWR, databaseFileMode)
	if errorValue != nil {
		return fmt.Errorf("open memory database %s: %w", path, errorValue)
	}
	return file.Close()
}

func applyMigrations(ctx context.Context, database *sql.DB) error {
	names, errorValue := fs.Glob(migrations.Files, "*.sql")
	if errorValue != nil {
		return errorValue
	}
	sort.Strings(names)
	var appliedCount int
	if errorValue := database.QueryRowContext(ctx, `pragma user_version`).Scan(&appliedCount); errorValue != nil {
		return errorValue
	}
	for index := appliedCount; index < len(names); index++ {
		if errorValue := applyMigration(ctx, database, names[index], index+1); errorValue != nil {
			return errorValue
		}
	}
	return nil
}

func applyMigration(ctx context.Context, database *sql.DB, name string, version int) error {
	statements, errorValue := fs.ReadFile(migrations.Files, name)
	if errorValue != nil {
		return errorValue
	}
	transaction, errorValue := database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	if _, errorValue := transaction.ExecContext(ctx, string(statements)); errorValue != nil {
		return fmt.Errorf("apply migration %s: %w", name, errorValue)
	}
	if _, errorValue := transaction.ExecContext(ctx, fmt.Sprintf(`pragma user_version = %d`, version)); errorValue != nil {
		return errorValue
	}
	return transaction.Commit()
}

func withDefaults(configuration Configuration) Configuration {
	if configuration.Capacity <= 0 {
		configuration.Capacity = DefaultCapacity
	}
	if configuration.StaticShare <= 0 || configuration.StaticShare > 1 {
		configuration.StaticShare = DefaultStaticShare
	}
	if configuration.TombstoneCapacity <= 0 {
		configuration.TombstoneCapacity = configuration.Capacity
	}
	if configuration.ColdGrace <= 0 {
		configuration.ColdGrace = DefaultColdGrace
	}
	if configuration.HalfLife <= 0 {
		configuration.HalfLife = DefaultHalfLife
	}
	if configuration.ClaimDuration <= 0 {
		configuration.ClaimDuration = DefaultClaimDuration
	}
	if configuration.Location == nil {
		configuration.Location = time.UTC
	}
	if configuration.Logger == nil {
		configuration.Logger = slog.Default()
	}
	return configuration
}

func (store *Store) now() time.Time {
	if store.configuration.Now != nil {
		return store.configuration.Now().UTC()
	}
	return time.Now().UTC()
}

const memoryColumns = `memory_id, content, is_static, occurred_at, valid_until, origin_id, importance, storage_strength,
	resolved_entity_ids, unresolved_names, created_at, last_recalled_at, cold_since, cold_reason, superseded_by`

type rowScanner interface {
	Scan(destinations ...any) error
}

func scanMemory(row rowScanner, extra ...any) (Memory, error) {
	var memory Memory
	var occurredAt, validUntil, lastRecalledAt, coldSince sql.NullInt64
	var coldReason, supersededBy sql.NullString
	var resolvedJSON, unresolvedJSON string
	var createdAt int64
	destinations := append([]any{&memory.MemoryID, &memory.Content, &memory.IsStatic, &occurredAt, &validUntil, &memory.OriginID,
		&memory.Importance, &memory.StorageStrength, &resolvedJSON, &unresolvedJSON, &createdAt, &lastRecalledAt, &coldSince,
		&coldReason, &supersededBy}, extra...)
	if errorValue := row.Scan(destinations...); errorValue != nil {
		return Memory{}, errorValue
	}
	if errorValue := json.Unmarshal([]byte(resolvedJSON), &memory.ResolvedEntityIDs); errorValue != nil {
		return Memory{}, fmt.Errorf("memory %s resolved_entity_ids: %w", memory.MemoryID, errorValue)
	}
	if errorValue := json.Unmarshal([]byte(unresolvedJSON), &memory.UnresolvedNames); errorValue != nil {
		return Memory{}, fmt.Errorf("memory %s unresolved_names: %w", memory.MemoryID, errorValue)
	}
	memory.OccurredAt = fromNullMilliseconds(occurredAt)
	memory.ValidUntil = fromNullMilliseconds(validUntil)
	memory.CreatedAt = fromMilliseconds(createdAt)
	memory.LastRecalledAt = fromNullMilliseconds(lastRecalledAt)
	memory.ColdSince = fromNullMilliseconds(coldSince)
	memory.ColdReason = ColdReason(coldReason.String)
	memory.SupersededBy = supersededBy.String
	return memory, nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
}

func queryMemories(ctx context.Context, source querier, clause string, arguments ...any) ([]Memory, error) {
	rows, errorValue := source.QueryContext(ctx, `select `+memoryColumns+` from memory `+clause, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	memories := []Memory{}
	for rows.Next() {
		memory, errorValue := scanMemory(rows)
		if errorValue != nil {
			return nil, errorValue
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func toMilliseconds(instant time.Time) int64 {
	return instant.UnixMilli()
}

func nullMilliseconds(instant time.Time) sql.NullInt64 {
	if instant.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: instant.UnixMilli(), Valid: true}
}

func fromMilliseconds(milliseconds int64) time.Time {
	return time.UnixMilli(milliseconds).UTC()
}

func fromNullMilliseconds(milliseconds sql.NullInt64) time.Time {
	if !milliseconds.Valid {
		return time.Time{}
	}
	return fromMilliseconds(milliseconds.Int64)
}
