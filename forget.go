package bluememo

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const ForgetCandidateLimit = 10

var ErrEmptyForgetTarget = errors.New("forget needs a target")

type ForgetOutcome struct {
	Forgotten  []Memory `json:"forgotten"`
	Candidates []Memory `json:"candidates"`
}

func (store *Store) Forget(ctx context.Context, target string) (ForgetOutcome, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return ForgetOutcome{}, ErrEmptyForgetTarget
	}
	resolved, errorValue := store.resolveForgetTarget(ctx, trimmed)
	if errorValue != nil {
		return ForgetOutcome{}, errorValue
	}
	if len(resolved) == 1 {
		return store.forgetResolved(ctx, resolved, trimmed)
	}
	if len(resolved) > 1 {
		return ForgetOutcome{Forgotten: []Memory{}, Candidates: resolved}, nil
	}
	candidates, errorValue := store.forgetCandidates(ctx, trimmed)
	return ForgetOutcome{Forgotten: []Memory{}, Candidates: candidates}, errorValue
}

func (store *Store) ForgetMemories(ctx context.Context, memoryIDs []string, requestPhrase string) ([]Memory, error) {
	memories := []Memory{}
	for _, memoryID := range memoryIDs {
		found, errorValue := queryMemories(ctx, store.database, `where memory_id = ?`, strings.TrimSpace(memoryID))
		if errorValue != nil {
			return nil, errorValue
		}
		memories = append(memories, found...)
	}
	outcome, errorValue := store.forgetResolved(ctx, memories, strings.TrimSpace(requestPhrase))
	return outcome.Forgotten, errorValue
}

func (store *Store) resolveForgetTarget(ctx context.Context, target string) ([]Memory, error) {
	byID, errorValue := queryMemories(ctx, store.database, `where memory_id = ?`, target)
	if errorValue != nil || len(byID) > 0 {
		return byID, errorValue
	}
	byContent, errorValue := queryMemories(ctx, store.database, `where content = ?`, target)
	if errorValue != nil || len(byContent) > 0 {
		return byContent, errorValue
	}
	return queryMemories(ctx, store.database, `where cold_since is null and instr(content, ?) > 0`, target)
}

func (store *Store) forgetCandidates(ctx context.Context, target string) ([]Memory, error) {
	search := searchQuery{text: target, limit: ForgetCandidateLimit, now: store.now()}
	search.embedding, _ = store.embedQuery(ctx, target)
	ranked, errorValue := store.search(ctx, search)
	if errorValue != nil {
		return nil, errorValue
	}
	candidates := make([]Memory, 0, min(ForgetCandidateLimit, len(ranked)))
	for _, entry := range ranked[:min(ForgetCandidateLimit, len(ranked))] {
		candidates = append(candidates, entry.Memory)
	}
	return candidates, nil
}

func (store *Store) forgetResolved(ctx context.Context, memories []Memory, requestPhrase string) (ForgetOutcome, error) {
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return ForgetOutcome{}, errorValue
	}
	defer transaction.Rollback()
	now := store.now()
	for _, memory := range memories {
		if errorValue := bury(ctx, transaction, memory, TombstoneReasonAsked, requestPhrase, now); errorValue != nil {
			return ForgetOutcome{}, errorValue
		}
	}
	return ForgetOutcome{Forgotten: memories, Candidates: []Memory{}}, transaction.Commit()
}

func bury(ctx context.Context, transaction *sql.Tx, memory Memory, reason TombstoneReason, requestPhrase string, now time.Time) error {
	if _, errorValue := transaction.ExecContext(ctx, `
		insert or replace into tombstone (memory_id, content, is_static, occurred_at, origin_id, reason, request_phrase, created_at, died_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		memory.MemoryID, memory.Content, memory.IsStatic, nullMilliseconds(memory.OccurredAt), memory.OriginID, reason,
		requestPhrase, toMilliseconds(memory.CreatedAt), toMilliseconds(now)); errorValue != nil {
		return errorValue
	}
	if _, errorValue := transaction.ExecContext(ctx, `update memory set superseded_by = null where superseded_by = ?`, memory.MemoryID); errorValue != nil {
		return errorValue
	}
	_, errorValue := transaction.ExecContext(ctx, `delete from memory where memory_id = ?`, memory.MemoryID)
	return errorValue
}
