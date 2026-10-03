package bluememo

import (
	"context"
	"database/sql"
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
	Embedder           Embedder
	Chooser            Chooser
	Reranker           Reranker
	RerankDepth        int
	LaneDepth          int
	EmbeddingModel     string
	Model              LanguageModel
	Judge              Judge
	Capacity           int
	StaticShare        float64
	TombstoneCapacity  int
	ColdGrace          time.Duration
	HalfLife           time.Duration
	ClaimDuration      time.Duration
	Location           *time.Location
	EmbedTimeReference bool
	RecallSources      bool
	RehearseTriggers   bool
	Logger             *slog.Logger
	Now                func() time.Time
	NewIdentifier      func() string
}

type Store struct {
	database      *sql.DB
	configuration Configuration
	beneath       []Known
}

type Note struct {
	GroupID     string
	Body        string
	SpeakerName string
	IsExplicit  bool
}

var (
	ErrEmptyNote = errors.New("a note needs a body")
	ErrNoPath    = errors.New("a memory store needs the path of its database file")
)

func Open(ctx context.Context, path string, configuration Configuration) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrNoPath
	}
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
		groupID = store.newIdentifier()
	}
	_, errorValue := store.database.ExecContext(ctx,
		`insert into pending_note (note_id, group_id, body, speaker_name, is_explicit, arrived_at) values (?, ?, ?, ?, ?, ?)`,
		store.newIdentifier(), groupID, body, strings.TrimSpace(note.SpeakerName), note.IsExplicit, toMilliseconds(store.now()))
	return errorValue
}

func (store *Store) Memories(ctx context.Context) ([]Memory, error) {
	return queryMemories(ctx, store.database, `where cold_since is null order by created_at desc`)
}

// IsEmpty says whether the store keeps nothing its owner could lose: no
// memory, warm or cold, no note waiting to be settled, and no file.
func (store *Store) IsEmpty(ctx context.Context) (bool, error) {
	var held int
	errorValue := store.database.QueryRowContext(ctx, `
		select (select count(*) from memory) + (select count(*) from pending_note) + (select count(*) from file)`).Scan(&held)
	return held == 0, errorValue
}

func (store *Store) Tombstones(ctx context.Context) ([]Tombstone, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select memory_id, content, is_static, occurred_at, occurred_until, origin_id, reason, request_phrase, created_at, died_at
		from tombstone order by died_at desc`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	tombstones := []Tombstone{}
	for rows.Next() {
		var tombstone Tombstone
		var occurredAt, occurredUntil sql.NullInt64
		var createdAt, diedAt int64
		if errorValue := rows.Scan(&tombstone.MemoryID, &tombstone.Content, &tombstone.IsStatic, &occurredAt, &occurredUntil, &tombstone.OriginID,
			&tombstone.Reason, &tombstone.RequestPhrase, &createdAt, &diedAt); errorValue != nil {
			return nil, errorValue
		}
		tombstone.OccurredAt = fromNullMilliseconds(occurredAt)
		tombstone.OccurredUntil = fromNullMilliseconds(occurredUntil)
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
	if configuration.Judge == nil && configuration.Chooser != nil {
		configuration.Judge = DistributionJudge{Chooser: configuration.Chooser}
	}
	if configuration.EmbeddingModel == "" {
		configuration.EmbeddingModel = embeddingModelNameOf(configuration.Embedder)
	}
	return configuration
}

// embeddingModelNameOf asks an embedder what it is. The name belongs to the
// embedder, and a configuration that carries its own copy is a second copy to
// keep in step: a name that drifts from the vectors it labels makes every one
// of them unreadable.
func embeddingModelNameOf(embedder Embedder) string {
	named, isNamed := embedder.(interface{ EmbeddingModelName() string })
	if !isNamed {
		return ""
	}
	return named.EmbeddingModelName()
}

func (store *Store) now() time.Time {
	if store.configuration.Now != nil {
		return store.configuration.Now().UTC()
	}
	return time.Now().UTC()
}

func (store *Store) newIdentifier() string {
	if store.configuration.NewIdentifier != nil {
		return store.configuration.NewIdentifier()
	}
	return NewIdentifier()
}

const memoryColumns = `memory_id, content, is_static, occurred_at, occurred_until, valid_until, origin_id, importance, storage_strength,
	created_at, last_recalled_at, cold_since, cold_reason, superseded_by`

type rowScanner interface {
	Scan(destinations ...any) error
}

func scanMemory(row rowScanner, extra ...any) (Memory, error) {
	var memory Memory
	var occurredAt, occurredUntil, validUntil, lastRecalledAt, coldSince sql.NullInt64
	var coldReason, supersededBy sql.NullString
	var createdAt int64
	destinations := append([]any{&memory.MemoryID, &memory.Content, &memory.IsStatic, &occurredAt, &occurredUntil, &validUntil, &memory.OriginID,
		&memory.Importance, &memory.StorageStrength, &createdAt, &lastRecalledAt, &coldSince,
		&coldReason, &supersededBy}, extra...)
	if errorValue := row.Scan(destinations...); errorValue != nil {
		return Memory{}, errorValue
	}
	memory.OccurredAt = fromNullMilliseconds(occurredAt)
	memory.OccurredUntil = fromNullMilliseconds(occurredUntil)
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
