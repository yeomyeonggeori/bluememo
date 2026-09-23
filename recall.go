package bluememo

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	SearchModeHybrid  = "hybrid"
	SearchModeLexical = "lexical"

	DefaultRecallLimit   = 12
	UnsettledNoteLimit   = 3
	laneDepthMultiplier  = 3
	reciprocalRankOffset = 60.0
)

var ErrEmptyQuery = errors.New("a recall needs a query")

type RecalledMemory struct {
	Memory      Memory  `json:"memory"`
	Score       float64 `json:"score"`
	VectorRank  int     `json:"vectorRank,omitempty"`
	LexicalRank int     `json:"lexicalRank,omitempty"`
	TriggerRank int     `json:"triggerRank,omitempty"`
	IsSibling   bool    `json:"isSibling,omitempty"`
}

type UnsettledNote struct {
	Body      string    `json:"body"`
	ArrivedAt time.Time `json:"arrivedAt"`
}

type RecallResult struct {
	Memories       []RecalledMemory `json:"memories"`
	Unsettled      []UnsettledNote  `json:"unsettled"`
	Mode           string           `json:"mode"`
	DegradedReason string           `json:"degradedReason,omitempty"`
}

type searchQuery struct {
	text      string
	embedding []float32
	limit     int
	now       time.Time
}

func (store *Store) Recall(ctx context.Context, query string, limit int) (RecallResult, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return RecallResult{}, ErrEmptyQuery
	}
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	search := searchQuery{text: trimmed, limit: limit, now: store.now()}
	result := RecallResult{Mode: SearchModeHybrid}
	search.embedding, result.DegradedReason = store.embedQuery(ctx, trimmed)
	if search.embedding == nil {
		result.Mode = SearchModeLexical
	}
	ranked, errorValue := store.search(ctx, search)
	if errorValue != nil {
		return RecallResult{}, errorValue
	}
	result.Memories, errorValue = store.withSiblings(ctx, ranked, limit)
	if errorValue != nil {
		return RecallResult{}, errorValue
	}
	if result.Unsettled, errorValue = store.unsettledNotes(ctx, trimmed); errorValue != nil {
		return RecallResult{}, errorValue
	}
	return result, store.reinforce(ctx, result.Memories, search.now)
}

func (store *Store) Profile(ctx context.Context) ([]Memory, error) {
	now := store.now()
	memories, errorValue := queryMemories(ctx, store.database,
		`where cold_since is null and is_static = 1 and (valid_until is null or valid_until > ?)`, toMilliseconds(now))
	if errorValue != nil {
		return nil, errorValue
	}
	sort.SliceStable(memories, func(left int, right int) bool {
		return Usefulness(memories[left], store.configuration.HalfLife, now) > Usefulness(memories[right], store.configuration.HalfLife, now)
	})
	return memories, nil
}

func (store *Store) embedQuery(ctx context.Context, text string) ([]float32, string) {
	if store.configuration.Embedder == nil {
		return nil, "no embedder is configured"
	}
	embedding, errorValue := store.configuration.Embedder.EmbedQuery(ctx, text)
	if errorValue != nil {
		return nil, "query embedding failed: " + errorValue.Error()
	}
	if errorValue := ValidateEmbedding(embedding); errorValue != nil {
		return nil, "query embedding rejected: " + errorValue.Error()
	}
	return embedding, ""
}

func (store *Store) search(ctx context.Context, query searchQuery) ([]RecalledMemory, error) {
	retrievable, errorValue := store.retrievable(ctx, query.now)
	if errorValue != nil {
		return nil, errorValue
	}
	depth := query.limit * laneDepthMultiplier
	byID := map[string]*RecalledMemory{}
	record := func(memoryID string, rank int, assign func(*RecalledMemory, int)) {
		held, isRetrievable := retrievable[memoryID]
		if !isRetrievable {
			return
		}
		entry, isPresent := byID[memoryID]
		if !isPresent {
			entry = &RecalledMemory{Memory: held.memory}
			byID[memoryID] = entry
		}
		assign(entry, rank)
		entry.Score += 1 / (reciprocalRankOffset + float64(rank))
	}
	for rank, memoryID := range rankByVector(retrievable, query.embedding, depth) {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.VectorRank = rank })
	}
	for rank, memoryID := range rankByLexeme(retrievable, query.text, depth) {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.LexicalRank = rank })
	}
	triggerIDs, errorValue := store.rankByTrigger(ctx, query.embedding, depth)
	if errorValue != nil {
		return nil, errorValue
	}
	for rank, memoryID := range triggerIDs {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.TriggerRank = rank })
	}
	return sortedByScore(byID), nil
}

func (store *Store) retrievable(ctx context.Context, now time.Time) (map[string]candidate, error) {
	rows, errorValue := store.database.QueryContext(ctx, `select `+memoryColumns+`,
		case when embedding_model = ? then embedding end
		from memory
		where (cold_since is null or cold_reason = ?) and (valid_until is null or valid_until > ?)`,
		store.configuration.EmbeddingModel, ColdReasonPressure, toMilliseconds(now))
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	retrievable := map[string]candidate{}
	for rows.Next() {
		var encoded []byte
		memory, errorValue := scanMemory(rows, &encoded)
		if errorValue != nil {
			return nil, errorValue
		}
		retrievable[memory.MemoryID] = candidate{memory: memory, embedding: decodeEmbedding(encoded)}
	}
	return retrievable, rows.Err()
}

func rankByVector(retrievable map[string]candidate, query []float32, depth int) []string {
	if query == nil {
		return nil
	}
	type scored struct {
		memoryID   string
		similarity float64
	}
	all := make([]scored, 0, len(retrievable))
	for memoryID, held := range retrievable {
		if len(held.embedding) == 0 {
			continue
		}
		all = append(all, scored{memoryID: memoryID, similarity: cosineSimilarity(query, held.embedding)})
	}
	sort.Slice(all, func(left int, right int) bool {
		if all[left].similarity != all[right].similarity {
			return all[left].similarity > all[right].similarity
		}
		return all[left].memoryID < all[right].memoryID
	})
	identifiers := make([]string, 0, min(depth, len(all)))
	for _, entry := range all[:min(depth, len(all))] {
		identifiers = append(identifiers, entry.memoryID)
	}
	return identifiers
}

func (store *Store) rankByTrigger(ctx context.Context, query []float32, depth int) ([]string, error) {
	if query == nil {
		return nil, nil
	}
	rows, errorValue := store.database.QueryContext(ctx, `
		select t.memory_id, t.embedding from memory_trigger t join memory m using (memory_id)
		where m.cold_since is null and t.embedding_model = ?`, store.configuration.EmbeddingModel)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	bestByMemory := map[string]float64{}
	for rows.Next() {
		var memoryID string
		var encoded []byte
		if errorValue := rows.Scan(&memoryID, &encoded); errorValue != nil {
			return nil, errorValue
		}
		similarity := cosineSimilarity(query, decodeEmbedding(encoded))
		if best, isPresent := bestByMemory[memoryID]; !isPresent || similarity > best {
			bestByMemory[memoryID] = similarity
		}
	}
	if errorValue := rows.Err(); errorValue != nil {
		return nil, errorValue
	}
	identifiers := make([]string, 0, len(bestByMemory))
	for memoryID := range bestByMemory {
		identifiers = append(identifiers, memoryID)
	}
	sort.Slice(identifiers, func(left int, right int) bool {
		if bestByMemory[identifiers[left]] != bestByMemory[identifiers[right]] {
			return bestByMemory[identifiers[left]] > bestByMemory[identifiers[right]]
		}
		return identifiers[left] < identifiers[right]
	})
	return identifiers[:min(depth, len(identifiers))], nil
}

func (store *Store) withSiblings(ctx context.Context, ranked []RecalledMemory, limit int) ([]RecalledMemory, error) {
	if len(ranked) == 0 {
		return []RecalledMemory{}, nil
	}
	leading := ranked[0]
	siblings, errorValue := queryMemories(ctx, store.database,
		`where origin_id = ? and memory_id <> ? and cold_since is null and (valid_until is null or valid_until > ?) order by created_at`,
		leading.Memory.OriginID, leading.Memory.MemoryID, toMilliseconds(store.now()))
	if errorValue != nil {
		return nil, errorValue
	}
	expanded := []RecalledMemory{leading}
	present := map[string]bool{leading.Memory.MemoryID: true}
	for _, sibling := range siblings {
		present[sibling.MemoryID] = true
		expanded = append(expanded, RecalledMemory{Memory: sibling, Score: leading.Score, IsSibling: true})
	}
	for _, entry := range ranked[1:] {
		if !present[entry.Memory.MemoryID] {
			expanded = append(expanded, entry)
		}
	}
	return expanded[:min(limit, len(expanded))], nil
}

func (store *Store) unsettledNotes(ctx context.Context, text string) ([]UnsettledNote, error) {
	rows, errorValue := store.database.QueryContext(ctx, `select body, arrived_at from pending_note where settled_at is null order by arrived_at`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	notes, bodies := []UnsettledNote{}, []string{}
	for rows.Next() {
		var note UnsettledNote
		var arrivedAt int64
		if errorValue := rows.Scan(&note.Body, &arrivedAt); errorValue != nil {
			return nil, errorValue
		}
		note.ArrivedAt = fromMilliseconds(arrivedAt)
		notes = append(notes, note)
		bodies = append(bodies, note.Body)
	}
	if errorValue := rows.Err(); errorValue != nil {
		return nil, errorValue
	}
	matching := []UnsettledNote{}
	for _, index := range rankByOverlap(text, bodies, UnsettledNoteLimit) {
		matching = append(matching, notes[index])
	}
	return matching, nil
}

func (store *Store) reinforce(ctx context.Context, recalled []RecalledMemory, now time.Time) error {
	if len(recalled) == 0 {
		return nil
	}
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	for _, entry := range recalled {
		gain := RecallReinforcement(entry.Memory, store.configuration.HalfLife, now)
		if errorValue := reinforceOne(ctx, transaction, entry.Memory.MemoryID, gain, now); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}

func reinforceOne(ctx context.Context, transaction *sql.Tx, memoryID string, gain float64, now time.Time) error {
	_, errorValue := transaction.ExecContext(ctx, `
		update memory set storage_strength = storage_strength + ?, last_recalled_at = ?,
		       cold_since = case when cold_reason = ? then null else cold_since end,
		       cold_reason = case when cold_reason = ? then null else cold_reason end
		where memory_id = ?`, gain, toMilliseconds(now), ColdReasonPressure, ColdReasonPressure, memoryID)
	return errorValue
}

func sortedByScore(byID map[string]*RecalledMemory) []RecalledMemory {
	ranked := make([]RecalledMemory, 0, len(byID))
	for _, entry := range byID {
		ranked = append(ranked, *entry)
	}
	sort.Slice(ranked, func(left int, right int) bool {
		if ranked[left].Score != ranked[right].Score {
			return ranked[left].Score > ranked[right].Score
		}
		return ranked[left].Memory.MemoryID < ranked[right].Memory.MemoryID
	})
	return ranked
}
