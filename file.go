package bluememo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type FileKind string

const (
	FileKindDocument FileKind = "document"
	FileKindImage    FileKind = "image"
	FileKindVideo    FileKind = "video"
	FileKindAudio    FileKind = "audio"
	FileKindOther    FileKind = "other"
)

var fileKinds = map[FileKind]bool{
	FileKindDocument: true,
	FileKindImage:    true,
	FileKindVideo:    true,
	FileKindAudio:    true,
	FileKindOther:    true,
}

type File struct {
	FileID     string
	Name       string
	Extension  string
	Kind       FileKind
	Summary    string
	Data       json.RawMessage
	Category   string
	Supersedes string
	CreatedAt  time.Time
}

type FileRequest struct {
	Text     string
	Category string
	Limit    int
}

type RecalledFile struct {
	File      File
	Relevance float64
}

var (
	ErrFileIDMissing   = errors.New("a file needs an identifier its host assigned")
	ErrFileNameMissing = errors.New("a file needs the name it currently carries")
	ErrSummaryMissing  = errors.New("a file needs a summary, which is what a search reads")
	ErrUnknownFileKind = errors.New("a file is a document, an image, a video, audio, or other")
	ErrFileNotFound    = errors.New("no file holds that identifier")
)

const fileColumns = `file_id, name, extension, kind, summary, data, category, supersedes, created_at`

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
	if !fileKinds[file.Kind] {
		return fmt.Errorf("%w: %q", ErrUnknownFileKind, file.Kind)
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
	embedding, errorValue := store.configuration.Embedder.EmbedDocuments(ctx, []string{file.Summary})
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
		insert into file (file_id, name, extension, kind, summary, data, category, supersedes, embedding_model, embedding, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict (file_id) do update set
			name = excluded.name, extension = excluded.extension, kind = excluded.kind,
			summary = excluded.summary, data = excluded.data, category = excluded.category,
			supersedes = excluded.supersedes, embedding_model = excluded.embedding_model,
			embedding = excluded.embedding`,
		file.FileID, file.Name, file.Extension, string(file.Kind), file.Summary, data,
		file.Category, nullableText(file.Supersedes), store.configuration.EmbeddingModel,
		encodeEmbedding(embedding[0]), toMilliseconds(created))
	return errorValue
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func scanFile(rows *sql.Rows, encoded *[]byte) (File, error) {
	var file File
	var kind string
	var data string
	var supersedes sql.NullString
	var created int64
	targets := []any{&file.FileID, &file.Name, &file.Extension, &kind, &file.Summary,
		&data, &file.Category, &supersedes, &created}
	if encoded != nil {
		targets = append(targets, encoded)
	}
	if errorValue := rows.Scan(targets...); errorValue != nil {
		return File{}, errorValue
	}
	file.Kind = FileKind(kind)
	file.Data = json.RawMessage(data)
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
	candidates, errorValue := store.currentFiles(ctx, request.Category)
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

func (store *Store) currentFiles(ctx context.Context, category string) ([]fileCandidate, error) {
	statement := `select ` + fileColumns + `, embedding from file
		where embedding_model = ?
		  and file_id not in (select supersedes from file where supersedes is not null)`
	arguments := []any{store.configuration.EmbeddingModel}
	if category != "" {
		statement += ` and category = ?`
		arguments = append(arguments, category)
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
