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

// rehearsalImportanceFloor keeps the write-time cost off items nobody will
// look for. Every trigger is a model call and an embedding.
const rehearsalImportanceFloor = 3

const (
	RelationSame      Relation = "same"
	RelationUpdates   Relation = "updates"
	RelationExtends   Relation = "extends"
	RelationUnrelated Relation = "unrelated"
	RelationNoise     Relation = "noise"
)

// Judgement is one typed decision about a fresh proposition.
type Judgement struct {
	Relation    Relation
	TargetIndex int
	// Importance rates the proposition itself, so that when two say the same
	// thing the better rendering is the one retrieval keeps.
	Importance int
}

// Judge decides how a fresh proposition stands to the memories already held.
type Judge interface {
	Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error)
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
	judgement := Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: 3}
	if len(candidates) > 0 {
		texts := make([]string, len(candidates))
		for index, candidate := range candidates {
			texts[index] = candidate.content
		}
		judgement, errorValue = judge.Judge(ctx, monad.Content, texts)
		if errorValue != nil {
			return "", errorValue
		}
	}
	relation, targetIndex := judgement.Relation, judgement.TargetIndex
	if targetIndex < 0 || targetIndex >= len(candidates) {
		if relation != RelationNoise {
			relation = RelationUnrelated
		}
	}

	switch relation {
	case RelationNoise:
		return RelationNoise, nil
	case RelationSame:
		return store.keepTheBetterOne(ctx, monad, originID, candidates[targetIndex], judgement.Importance)
	}

	memoryID, errorValue := store.insertMonadWithImportance(ctx, monad, originID, judgement.Importance)
	if errorValue != nil {
		return "", errorValue
	}
	if judgement.Importance >= rehearsalImportanceFloor {
		if errorValue := store.rehearse(ctx, memoryID, monad.Content); errorValue != nil {
			return "", errorValue
		}
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

type candidate struct {
	memoryID   string
	content    string
	importance int
}

func (store *Store) nearest(ctx context.Context, text string, limit int) ([]candidate, error) {
	vectors, errorValue := store.embedder.Embed(ctx, []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	rows, errorValue := store.database.QueryContext(ctx,
		`select m.memory_id, m.content, coalesce(m.importance,3), v.vector from memory m join memory_vector v using(memory_id)
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
		if errorValue := rows.Scan(&one.memoryID, &one.content, &one.importance, &blob); errorValue != nil {
			return nil, errorValue
		}
		one.score = cosineSimilarity(vectors[0], decodeVector(blob))
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

// keepTheBetterOne resolves a same judgement by importance. The loser is
// superseded rather than deleted, so the wording that lost stays recoverable
// and only the better rendering reaches retrieval.
func (store *Store) keepTheBetterOne(ctx context.Context, monad Monad, originID string, held candidate, freshImportance int) (Relation, error) {
	if freshImportance <= held.importance {
		_, errorValue := store.database.ExecContext(ctx,
			`update memory set storage_strength = storage_strength + 0.25,
			 retrieval_strength = retrieval_strength + 0.5 where memory_id = ?`,
			held.memoryID)
		return RelationSame, errorValue
	}
	memoryID, errorValue := store.insertMonadWithImportance(ctx, monad, originID, freshImportance)
	if errorValue != nil {
		return "", errorValue
	}
	if _, errorValue := store.database.ExecContext(ctx,
		`update memory set superseded_by = ? where memory_id = ?`, memoryID, held.memoryID); errorValue != nil {
		return "", errorValue
	}
	_, errorValue = store.database.ExecContext(ctx,
		`update memory set storage_strength = storage_strength + 0.25,
		 retrieval_strength = retrieval_strength + 0.5 where memory_id = ?`, memoryID)
	return RelationSame, errorValue
}

func (store *Store) insertMonad(ctx context.Context, monad Monad, originID string) (string, error) {
	return store.insertMonadWithImportance(ctx, monad, originID, 3)
}

func (store *Store) insertMonadWithImportance(ctx context.Context, monad Monad, originID string, importance int) (string, error) {
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
		`insert into memory(memory_id,content,kind,valid_until,resolved_entity_ids,unresolved_names,created_at,origin_id,importance)
		 values(?,?,?,?,?,?,?,?,?)`,
		memoryID, monad.Content, string(monad.settledKind()), validUntil,
		string(resolvedJSON), string(unresolvedJSON),
		time.Now().UTC().Format(time.RFC3339Nano), originID, importance); errorValue != nil {
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
