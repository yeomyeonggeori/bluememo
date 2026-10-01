package bluememo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

type Medium string

const (
	MediumDocument Medium = "document"
	MediumImage    Medium = "image"
	MediumVideo    Medium = "video"
	MediumAudio    Medium = "audio"
	MediumOther    Medium = "other"
)

var media = map[Medium]bool{
	MediumDocument: true,
	MediumImage:    true,
	MediumVideo:    true,
	MediumAudio:    true,
	MediumOther:    true,
}

type File struct {
	FileID        string
	Name          string
	Extension     string
	Medium        Medium
	Summary       string
	Data          json.RawMessage
	Category      string
	OccurredAt    time.Time
	OccurredUntil time.Time
	Supersedes    string
	CreatedAt     time.Time
}

func (file File) TimeReference(location *time.Location) string {
	if file.OccurredAt.IsZero() {
		return ""
	}
	return timeReference(file.OccurredAt, file.OccurredUntil, location)
}

type FileRequest struct {
	Text       string
	Categories []string
	Limit      int
}

type RecalledFile struct {
	File      File
	Relevance float64
}

var (
	ErrFileIDMissing              = errors.New("a file needs an identifier its host assigned")
	ErrFileNameMissing            = errors.New("a file needs the name it currently carries")
	ErrSummaryMissing             = errors.New("a file needs a summary, which is what a search reads")
	ErrUnknownMedium              = errors.New("a file is a document, an image, a video, audio, or other")
	ErrFileNotFound               = errors.New("no file holds that identifier")
	ErrOccurrenceEndsWithoutStart = errors.New("a file that ends at a time must begin at one")
	ErrCategoryCode               = errors.New("a category is one to four ASCII letters or digits, one per level")
)

const CategoryCodeLimit = 4

func isCategoryCode(category string) bool {
	if len(category) > CategoryCodeLimit {
		return false
	}
	for _, character := range []byte(category) {
		isDigit := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'z'
		isUpper := character >= 'A' && character <= 'Z'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

const fileColumns = `file_id, name, extension, medium, summary, data, category, occurred_at, occurred_until, supersedes, created_at`

func validateFile(file File) error {
	if file.FileID == "" {
		return ErrFileIDMissing
	}
	if file.Name == "" {
		return ErrFileNameMissing
	}
	if file.Summary == "" {
		return ErrSummaryMissing
	}
	if !media[file.Medium] {
		return fmt.Errorf("%w: %q", ErrUnknownMedium, file.Medium)
	}
	if !file.OccurredUntil.IsZero() && file.OccurredAt.IsZero() {
		return ErrOccurrenceEndsWithoutStart
	}
	if !isCategoryCode(file.Category) {
		return fmt.Errorf("%w: %q", ErrCategoryCode, file.Category)
	}
	return nil
}

func fileData(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("the data of file is not JSON")
	}
	return string(data), nil
}

func (store *Store) StoreFile(ctx context.Context, file File) error {
	if errorValue := validateFile(file); errorValue != nil {
		return errorValue
	}
	data, errorValue := fileData(file.Data)
	if errorValue != nil {
		return errorValue
	}
	embedding, errorValue := store.configuration.Embedder.EmbedDocuments(ctx, []string{store.matchableText(file.Summary, file.OccurredAt, file.OccurredUntil)})
	if errorValue != nil {
		return fmt.Errorf("file summary embedding failed: %w", errorValue)
	}
	if len(embedding) != 1 {
		return fmt.Errorf("embedder returned %d embeddings for one file summary", len(embedding))
	}
	created := file.CreatedAt
	if created.IsZero() {
		created = store.now()
	}
	_, errorValue = store.database.ExecContext(ctx, `
		insert into file (file_id, name, extension, medium, summary, data, category,
			occurred_at, occurred_until, supersedes, embedding_model, embedding, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict (file_id) do update set
			name = excluded.name, extension = excluded.extension, medium = excluded.medium,
			summary = excluded.summary, data = excluded.data, category = excluded.category,
			occurred_at = excluded.occurred_at, occurred_until = excluded.occurred_until,
			supersedes = excluded.supersedes, embedding_model = excluded.embedding_model,
			embedding = excluded.embedding`,
		file.FileID, file.Name, file.Extension, string(file.Medium), file.Summary, data,
		file.Category, nullableInstant(file.OccurredAt), nullableInstant(file.OccurredUntil),
		nullableText(file.Supersedes), store.configuration.EmbeddingModel,
		encodeEmbedding(embedding[0]), toMilliseconds(created))
	return errorValue
}

func nullableInstant(instant time.Time) any {
	if instant.IsZero() {
		return nil
	}
	return toMilliseconds(instant)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func scanFile(rows *sql.Rows, encoded *[]byte) (File, error) {
	var file File
	var medium string
	var data string
	var occurredAt sql.NullInt64
	var occurredUntil sql.NullInt64
	var supersedes sql.NullString
	var created int64
	targets := []any{&file.FileID, &file.Name, &file.Extension, &medium, &file.Summary,
		&data, &file.Category, &occurredAt, &occurredUntil, &supersedes, &created}
	if encoded != nil {
		targets = append(targets, encoded)
	}
	if errorValue := rows.Scan(targets...); errorValue != nil {
		return File{}, errorValue
	}
	file.Medium = Medium(medium)
	file.Data = json.RawMessage(data)
	if occurredAt.Valid {
		file.OccurredAt = fromMilliseconds(occurredAt.Int64)
	}
	if occurredUntil.Valid {
		file.OccurredUntil = fromMilliseconds(occurredUntil.Int64)
	}
	file.Supersedes = supersedes.String
	file.CreatedAt = fromMilliseconds(created)
	return file, nil
}

func (store *Store) File(ctx context.Context, fileID string) (File, error) {
	rows, errorValue := store.database.QueryContext(ctx,
		`select `+fileColumns+` from file where file_id = ?`, fileID)
	if errorValue != nil {
		return File{}, errorValue
	}
	defer rows.Close()
	if !rows.Next() {
		if errorValue := rows.Err(); errorValue != nil {
			return File{}, errorValue
		}
		return File{}, ErrFileNotFound
	}
	return scanFile(rows, nil)
}

func (store *Store) RecallFiles(ctx context.Context, request FileRequest) ([]RecalledFile, error) {
	if request.Text == "" {
		return nil, ErrEmptyQuery
	}
	query, errorValue := store.configuration.Embedder.EmbedQuery(ctx, request.Text)
	if errorValue != nil {
		return nil, fmt.Errorf("file query embedding failed: %w", errorValue)
	}
	candidates, errorValue := store.currentFiles(ctx, request.Categories)
	if errorValue != nil {
		return nil, errorValue
	}
	ranked := make([]RecalledFile, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, RecalledFile{
			File:      candidate.file,
			Relevance: cosineSimilarity(query, candidate.embedding),
		})
	}
	sort.SliceStable(ranked, func(left int, right int) bool {
		if ranked[left].Relevance != ranked[right].Relevance {
			return ranked[left].Relevance > ranked[right].Relevance
		}
		return ranked[left].File.FileID < ranked[right].File.FileID
	})
	limit := request.Limit
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

type fileCandidate struct {
	file      File
	embedding []float32
}

func widestCategories(categories []string) []string {
	widest := make([]string, 0, len(categories))
	for _, category := range categories {
		if category == "" {
			continue
		}
		isCovered := false
		for _, other := range categories {
			if other != "" && other != category && strings.HasPrefix(category, other) {
				isCovered = true
				break
			}
		}
		if !isCovered && !slices.Contains(widest, category) {
			widest = append(widest, category)
		}
	}
	return widest
}

func (store *Store) currentFiles(ctx context.Context, categories []string) ([]fileCandidate, error) {
	statement := `select ` + fileColumns + `, embedding from file
		where embedding_model = ?
		  and file_id not in (select supersedes from file where supersedes is not null)`
	arguments := []any{store.configuration.EmbeddingModel}
	if scopes := widestCategories(categories); len(scopes) > 0 {
		clauses := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			clauses = append(clauses, `substr(category, 1, ?) = ?`)
			arguments = append(arguments, len(scope), scope)
		}
		statement += ` and (` + strings.Join(clauses, " or ") + `)`
	}
	rows, errorValue := store.database.QueryContext(ctx, statement, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	var candidates []fileCandidate
	for rows.Next() {
		var encoded []byte
		file, errorValue := scanFile(rows, &encoded)
		if errorValue != nil {
			return nil, errorValue
		}
		candidates = append(candidates, fileCandidate{file: file, embedding: decodeEmbedding(encoded)})
	}
	return candidates, rows.Err()
}
