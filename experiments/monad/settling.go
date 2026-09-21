package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const settlingSchema = `
create table if not exists pending (
  pending_id text primary key,
  group_id   text not null,
  body       text not null,
  arrived_at text not null,
  settled_at text
);

create table if not exists relation (
  from_memory_id text not null references memory(memory_id),
  to_memory_id   text not null references memory(memory_id),
  edge           text not null check (edge in ('updates','extends')),
  primary key (from_memory_id, to_memory_id)
);
`

type Relation string

const (
	RelationSame      Relation = "same"
	RelationUpdates   Relation = "updates"
	RelationExtends   Relation = "extends"
	RelationUnrelated Relation = "unrelated"
	RelationNoise     Relation = "noise"
)

// Judge decides how a fresh proposition stands to the memories already held.
type Judge interface {
	Judge(ctx context.Context, proposition string, candidates []string) (Relation, int, error)
}

// Enqueue accepts input without deciding anything about it. Search reaches it
// through the lexical lane until settling turns it into propositions.
func (store *Store) Enqueue(ctx context.Context, groupID string, body string) error {
	_, errorValue := store.database.ExecContext(ctx,
		`insert into pending(pending_id, group_id, body, arrived_at) values(?,?,?,?)`,
		newIdentifier(), groupID, body, time.Now().UTC().Format(time.RFC3339Nano))
	return errorValue
}

type SettleReport struct {
	Groups     int
	Proposed   int
	Inserted   int
	Reinforced int
	Superseded int
	Extended   int
	Dropped    int
}

// Settle turns everything pending into memories, one group at a time so the
// decomposer sees a coherent unit rather than isolated writes.
func (store *Store) Settle(ctx context.Context, judge Judge) (SettleReport, error) {
	report := SettleReport{}
	rows, errorValue := store.database.QueryContext(ctx,
		`select group_id, group_concat(body, '
') from pending where settled_at is null group by group_id order by min(arrived_at)`)
	if errorValue != nil {
		return report, errorValue
	}
	type batch struct{ groupID, body string }
	batches := []batch{}
	for rows.Next() {
		one := batch{}
		if errorValue := rows.Scan(&one.groupID, &one.body); errorValue != nil {
			rows.Close()
			return report, errorValue
		}
		batches = append(batches, one)
	}
	rows.Close()

	for _, one := range batches {
		report.Groups++
		monads, errorValue := store.decomposer.Decompose(ctx, one.body)
		if errorValue != nil {
			return report, fmt.Errorf("decompose group %s: %w", one.groupID, errorValue)
		}
		originID := newIdentifier()
		for _, monad := range monads {
			if validateMonad(monad) != nil {
				continue
			}
			report.Proposed++
			applied, errorValue := store.settleProposition(ctx, judge, monad, originID)
			if errorValue != nil {
				return report, errorValue
			}
			switch applied {
			case RelationSame:
				report.Reinforced++
			case RelationUpdates:
				report.Superseded++
			case RelationExtends:
				report.Extended++
			case RelationNoise:
				report.Dropped++
			default:
				report.Inserted++
			}
		}
		if _, errorValue := store.database.ExecContext(ctx,
			`update pending set settled_at = ? where group_id = ? and settled_at is null`,
			time.Now().UTC().Format(time.RFC3339Nano), one.groupID); errorValue != nil {
			return report, errorValue
		}
	}
	return report, nil
}

func (store *Store) settleProposition(ctx context.Context, judge Judge, monad Monad, originID string) (Relation, error) {
	candidates, errorValue := store.nearest(ctx, monad.Content, 5)
	if errorValue != nil {
		return "", errorValue
	}
	relation := RelationUnrelated
	targetIndex := -1
	if len(candidates) > 0 {
		texts := make([]string, len(candidates))
		for index, candidate := range candidates {
			texts[index] = candidate.content
		}
		relation, targetIndex, errorValue = judge.Judge(ctx, monad.Content, texts)
		if errorValue != nil {
			return "", errorValue
		}
	}
	if targetIndex < 0 || targetIndex >= len(candidates) {
		if relation != RelationNoise {
			relation = RelationUnrelated
		}
	}

	if relation == RelationSame && candidates[targetIndex].similarity < sameSimilarityFloor {
		relation = RelationUnrelated
	}

	switch relation {
	case RelationNoise:
		return RelationNoise, nil
	case RelationSame:
		_, errorValue := store.database.ExecContext(ctx,
			`update memory set storage_strength = storage_strength + 0.25,
			 retrieval_strength = retrieval_strength + 0.5 where memory_id = ?`,
			candidates[targetIndex].memoryID)
		return RelationSame, errorValue
	}

	memoryID, errorValue := store.insertMonad(ctx, monad, originID)
	if errorValue != nil {
		return "", errorValue
	}
	switch relation {
	case RelationUpdates:
		if _, errorValue := store.database.ExecContext(ctx,
			`update memory set superseded_by = ? where memory_id = ?`,
			memoryID, candidates[targetIndex].memoryID); errorValue != nil {
			return "", errorValue
		}
		_, errorValue = store.database.ExecContext(ctx,
			`insert or ignore into relation(from_memory_id,to_memory_id,edge) values(?,?,'updates')`,
			memoryID, candidates[targetIndex].memoryID)
		return RelationUpdates, errorValue
	case RelationExtends:
		_, errorValue = store.database.ExecContext(ctx,
			`insert or ignore into relation(from_memory_id,to_memory_id,edge) values(?,?,'extends')`,
			memoryID, candidates[targetIndex].memoryID)
		return RelationExtends, errorValue
	}
	return RelationUnrelated, nil
}

// sameSimilarityFloor gates the one judgement that destroys information.
// Collapsing two memories is not reversible; keeping a duplicate is.
const sameSimilarityFloor = 0.82

type candidate struct {
	memoryID   string
	content    string
	similarity float64
}

func (store *Store) nearest(ctx context.Context, text string, limit int) ([]candidate, error) {
	vectors, errorValue := store.embedder.Embed(ctx, []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	rows, errorValue := store.database.QueryContext(ctx,
		`select m.memory_id, m.content, v.vector from memory m join memory_vector v using(memory_id)
		 where m.superseded_by is null and m.forgotten_at is null`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	type scored struct {
		candidate
		score float64
	}
	all := []scored{}
	for rows.Next() {
		one := scored{}
		blob := []byte{}
		if errorValue := rows.Scan(&one.memoryID, &one.content, &blob); errorValue != nil {
			return nil, errorValue
		}
		one.score = cosineSimilarity(vectors[0], decodeVector(blob))
		one.similarity = one.score
		all = append(all, one)
	}
	for outer := 0; outer < len(all); outer++ {
		for inner := outer + 1; inner < len(all); inner++ {
			if all[inner].score > all[outer].score {
				all[outer], all[inner] = all[inner], all[outer]
			}
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	result := make([]candidate, len(all))
	for index, one := range all {
		result[index] = one.candidate
	}
	return result, nil
}

func (store *Store) insertMonad(ctx context.Context, monad Monad, originID string) (string, error) {
	memoryID := newIdentifier()
	resolved, unresolved := store.resolver.Resolve(monad.Content)
	resolvedJSON, _ := json.Marshal(resolved)
	unresolvedJSON, _ := json.Marshal(unresolved)
	vectors, errorValue := store.embedder.Embed(ctx, []string{monad.Content})
	if errorValue != nil {
		return "", errorValue
	}
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return "", errorValue
	}
	defer transaction.Rollback()
	validUntil := sql.NullString{String: monad.ValidUntil, Valid: strings.TrimSpace(monad.ValidUntil) != ""}
	if _, errorValue := transaction.ExecContext(ctx,
		`insert into memory(memory_id,content,kind,valid_until,resolved_entity_ids,unresolved_names,created_at,origin_id)
		 values(?,?,?,?,?,?,?,?)`,
		memoryID, monad.Content, string(monad.settledKind()), validUntil,
		string(resolvedJSON), string(unresolvedJSON),
		time.Now().UTC().Format(time.RFC3339Nano), originID); errorValue != nil {
		return "", errorValue
	}
	if _, errorValue := transaction.ExecContext(ctx,
		`insert into memory_search(content, memory_id) values(?,?)`, monad.Content, memoryID); errorValue != nil {
		return "", errorValue
	}
	if _, errorValue := transaction.ExecContext(ctx,
		`insert into memory_vector(memory_id, vector) values(?,?)`, memoryID, encodeVector(vectors[0])); errorValue != nil {
		return "", errorValue
	}
	return memoryID, transaction.Commit()
}
