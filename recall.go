package bluememo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SearchModeHybrid  = "hybrid"
	SearchModeLexical = "lexical"

	RerankFailureReason = "rerank failed: "

	DefaultRecallLimit   = 50
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
	Relevance      float64          `json:"relevance"`
	Sources        []RecalledSource `json:"sources,omitempty"`
	Unsettled      []UnsettledNote  `json:"unsettled"`
	Mode           string           `json:"mode"`
	DegradedReason string           `json:"degradedReason,omitempty"`
}

type RecalledSource struct {
	OriginID string `json:"originID"`
	Body     string `json:"body"`
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
	ranked, closest, errorValue := store.search(ctx, search)
	if errorValue != nil {
		return RecallResult{}, errorValue
	}
	result.Relevance = closest
	ranked, rerankFailure := store.rerank(ctx, trimmed, ranked, limit)
	result.DegradedReason = joinReasons(result.DegradedReason, rerankFailure)
	result.Memories, errorValue = store.withSiblings(ctx, ranked, limit)
	if errorValue != nil {
		return RecallResult{}, errorValue
	}
	if result.Sources, errorValue = store.sourcesOf(ctx, result.Memories); errorValue != nil {
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

func (store *Store) search(ctx context.Context, query searchQuery) ([]RecalledMemory, float64, error) {
	retrievable, errorValue := store.retrievable(ctx, query.now)
	if errorValue != nil {
		return nil, 0, errorValue
	}
	depth := store.laneDepth(query.limit)
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
	vectorIDs, closest := rankByVector(retrievable, query.embedding, depth)
	for rank, memoryID := range vectorIDs {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.VectorRank = rank })
	}
	for rank, memoryID := range rankByLexeme(retrievable, query.text, depth) {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.LexicalRank = rank })
	}
	triggerIDs, errorValue := store.rankByTrigger(ctx, query.embedding, depth)
	if errorValue != nil {
		return nil, 0, errorValue
	}
	for rank, memoryID := range triggerIDs {
		record(memoryID, rank+1, func(entry *RecalledMemory, rank int) { entry.TriggerRank = rank })
	}
	return sortedByScore(byID), closest, nil
}

// laneDepth is how deep each lane looks before the lanes are fused. A gold
// answer this benchmark cannot reach sits below it, so widening the pool is
// what lets a reranker see a candidate at all; it costs candidates, never
// answer context.
func (store *Store) laneDepth(limit int) int {
	if store.configuration.LaneDepth > 0 {
		return store.configuration.LaneDepth
	}
	return limit * laneDepthMultiplier
}

func (store *Store) rerankDepth(limit int) int {
	if store.configuration.RerankDepth > 0 {
		return store.configuration.RerankDepth
	}
	return store.laneDepth(limit)
}

func joinReasons(reasons ...string) string {
	present := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason != "" {
			present = append(present, reason)
		}
	}
	return strings.Join(present, "; ")
}

func (store *Store) rerank(ctx context.Context, query string, ranked []RecalledMemory, limit int) ([]RecalledMemory, string) {
	if store.configuration.Reranker == nil || len(ranked) == 0 {
		return ranked, ""
	}
	shortlist := ranked[:min(store.rerankDepth(limit), len(ranked))]
	contents := make([]string, len(shortlist))
	for index, entry := range shortlist {
		contents[index] = store.matchableText(entry.Memory.Content, entry.Memory.OccurredAt, entry.Memory.OccurredUntil)
	}
	scores, errorValue := store.configuration.Reranker.Rerank(ctx, query, contents)
	if errorValue != nil {
		return ranked, RerankFailureReason + errorValue.Error()
	}
	if len(scores) != len(shortlist) {
		return ranked, fmt.Sprintf("%sreturned %d scores for %d candidates", RerankFailureReason, len(scores), len(shortlist))
	}
	return append(reorderedByScores(shortlist, scores), ranked[len(shortlist):]...), ""
}

func reorderedByScores(shortlist []RecalledMemory, scores []float64) []RecalledMemory {
	order := make([]int, len(shortlist))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left int, right int) bool { return scores[order[left]] > scores[order[right]] })
	reordered := make([]RecalledMemory, len(shortlist))
	for position, index := range order {
		reordered[position] = shortlist[index]
	}
	return reordered
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

func rankByVector(retrievable map[string]candidate, query []float32, depth int) ([]string, float64) {
	if query == nil {
		return nil, 0
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
	if len(all) == 0 {
		return identifiers, 0
	}
	return identifiers, all[0].similarity
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

func (store *Store) sourcesOf(ctx context.Context, recalled []RecalledMemory) ([]RecalledSource, error) {
	if !store.configuration.RecallSources || len(recalled) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(recalled))
	origins := make([]any, 0, len(recalled))
	for _, entry := range recalled {
		if entry.Memory.OriginID == "" || seen[entry.Memory.OriginID] {
			continue
		}
		seen[entry.Memory.OriginID] = true
		origins = append(origins, entry.Memory.OriginID)
	}
	if len(origins) == 0 {
		return nil, nil
	}
	statement := `select origin_id, group_concat(body, char(10)) from
		(select origin_id, body from pending_note where origin_id in (?` + strings.Repeat(", ?", len(origins)-1) + `) order by arrived_at, note_id)
		group by origin_id`
	rows, errorValue := store.database.QueryContext(ctx, statement, origins...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	bodies := make(map[string]string, len(origins))
	for rows.Next() {
		var originID, body string
		if errorValue := rows.Scan(&originID, &body); errorValue != nil {
			return nil, errorValue
		}
		bodies[originID] = body
	}
	if errorValue := rows.Err(); errorValue != nil {
		return nil, errorValue
	}
	sources := make([]RecalledSource, 0, len(origins))
	for _, origin := range origins {
		originID := origin.(string)
		if body, isKnown := bodies[originID]; isKnown {
			sources = append(sources, RecalledSource{OriginID: originID, Body: body})
		}
	}
	return sources, nil
}
