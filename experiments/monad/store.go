package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

const reciprocalRankOffset = 60.0

type Store struct {
	database     *sql.DB
	decomposer   Decomposer
	embedder     Embedder
	triggerMaker TriggerMaker
	resolver     EntityResolver
	now          func() time.Time
}

type Ranked struct {
	Memory Memory
	Score  float64
}

type ForgetOutcome struct {
	Forgotten     []Memory
	NeedsApproval []Ranked
}

func openStore(path string, decomposer Decomposer, embedder Embedder, resolver EntityResolver, now func() time.Time) (*Store, error) {
	database, errorValue := sql.Open("sqlite", path)
	if errorValue != nil {
		return nil, errorValue
	}
	if _, errorValue = database.Exec(schemaStatements + settlingSchema + triggerSchema); errorValue != nil {
		return nil, errorValue
	}
	return &Store{database: database, decomposer: decomposer, embedder: embedder, resolver: resolver, now: now}, nil
}

// Memorize, Recall and Forget all begin the same way: natural language becomes
// an array of monads. The decomposer does not know which one called it.
func (store *Store) Memorize(ctx context.Context, text string) ([]Memory, error) {
	monads, errorValue := store.decomposer.Decompose(ctx, text)
	if errorValue != nil {
		return nil, errorValue
	}
	if len(monads) == 0 {
		return nil, nil
	}

	contents := make([]string, len(monads))
	for index, monad := range monads {
		contents[index] = monad.Content
	}
	vectors, errorValue := store.embedder.Embed(ctx, contents)
	if errorValue != nil {
		return nil, errorValue
	}

	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return nil, errorValue
	}
	defer transaction.Rollback()

	stored := make([]Memory, 0, len(monads))
	for index, monad := range monads {
		resolved, unresolved := store.resolver.Resolve(monad.Content)
		memory := Memory{
			Monad:             Monad{Content: monad.Content, Kind: monad.settledKind(), ValidUntil: monad.ValidUntil},
			MemoryID:          newIdentifier(),
			ResolvedEntityIDs: resolved,
			UnresolvedNames:   unresolved,
			StorageStrength:   1.0,
			RetrievalStrength: 1.0,
			CreatedAt:         store.now(),
		}
		if errorValue = insertMemory(ctx, transaction, memory, vectors[index]); errorValue != nil {
			return nil, errorValue
		}
		stored = append(stored, memory)
	}
	return stored, transaction.Commit()
}

func (store *Store) Recall(ctx context.Context, text string, limit int) ([]Ranked, error) {
	monads, errorValue := store.decomposer.Decompose(ctx, text)
	if errorValue != nil {
		return nil, errorValue
	}
	if len(monads) == 0 {
		monads = []Monad{{Content: text, Kind: KindUnknown}}
	}
	ranked, errorValue := store.search(ctx, monads, limit)
	if errorValue != nil {
		return nil, errorValue
	}
	return ranked, store.markRecalled(ctx, ranked)
}

// Forget climbs the resolver ladder: one match is removed, several are returned
// for confirmation, none is reported as none. Fuzzy scores only order a
// question; they never settle what disappears.
func (store *Store) Forget(ctx context.Context, text string) (ForgetOutcome, error) {
	monads, errorValue := store.decomposer.Decompose(ctx, text)
	if errorValue != nil {
		return ForgetOutcome{}, errorValue
	}
	ranked, errorValue := store.search(ctx, monads, 10)
	if errorValue != nil {
		return ForgetOutcome{}, errorValue
	}
	if len(ranked) == 0 {
		return ForgetOutcome{}, nil
	}
	if len(ranked) > 1 && ranked[0].Score < ranked[1].Score*1.5 {
		return ForgetOutcome{NeedsApproval: ranked}, nil
	}
	if errorValue = store.bury(ctx, ranked[0].Memory, "asked"); errorValue != nil {
		return ForgetOutcome{}, errorValue
	}
	return ForgetOutcome{Forgotten: []Memory{ranked[0].Memory}}, nil
}

func (store *Store) search(ctx context.Context, monads []Monad, limit int) ([]Ranked, error) {
	contents := make([]string, len(monads))
	for index, monad := range monads {
		contents[index] = monad.Content
	}
	vectors, errorValue := store.embedder.Embed(ctx, contents)
	if errorValue != nil {
		return nil, errorValue
	}

	live, errorValue := store.loadLive(ctx)
	if errorValue != nil {
		return nil, errorValue
	}

	scoreByID := map[string]float64{}
	for index := range monads {
		for _, memoryID := range store.rankByVector(live, vectors[index], limit*3) {
			scoreByID[memoryID.id] += 1.0 / (reciprocalRankOffset + float64(memoryID.rank))
		}
		lexical, errorValue := store.rankByLexeme(ctx, contents[index], limit*3)
		if errorValue != nil {
			return nil, errorValue
		}
		for _, memoryID := range lexical {
			scoreByID[memoryID.id] += 1.0 / (reciprocalRankOffset + float64(memoryID.rank))
		}
		reached, errorValue := store.rankByTrigger(ctx, vectors[index], limit*3)
		if errorValue != nil {
			return nil, errorValue
		}
		for _, hit := range reached {
			scoreByID[hit.memoryID] += 1.0 / (reciprocalRankOffset + float64(hit.rank))
		}
	}

	ranked := make([]Ranked, 0, len(scoreByID))
	for memoryID, score := range scoreByID {
		memory, found := live[memoryID]
		if !found {
			continue
		}
		ranked = append(ranked, Ranked{Memory: memory.memory, Score: score})
	}
	sort.Slice(ranked, func(first int, second int) bool {
		if ranked[first].Score != ranked[second].Score {
			return ranked[first].Score > ranked[second].Score
		}
		return ranked[first].Memory.MemoryID < ranked[second].Memory.MemoryID
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

type rankedIdentifier struct {
	id   string
	rank int
}

type liveMemory struct {
	memory Memory
	vector []float32
}

// Brute force over every live vector. At one person's scale this is exact and
// fast, so no approximate index and no pre-filter recall collapse.
func (store *Store) rankByVector(live map[string]liveMemory, query []float32, limit int) []rankedIdentifier {
	type scored struct {
		id         string
		similarity float64
	}
	all := make([]scored, 0, len(live))
	for memoryID, candidate := range live {
		all = append(all, scored{id: memoryID, similarity: cosineSimilarity(query, candidate.vector)})
	}
	sort.Slice(all, func(first int, second int) bool { return all[first].similarity > all[second].similarity })
	if len(all) > limit {
		all = all[:limit]
	}
	ranked := make([]rankedIdentifier, len(all))
	for index, candidate := range all {
		ranked[index] = rankedIdentifier{id: candidate.id, rank: index + 1}
	}
	return ranked
}

func (store *Store) rankByLexeme(ctx context.Context, query string, limit int) ([]rankedIdentifier, error) {
	terms := lexicalTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	rows, errorValue := store.database.QueryContext(ctx, `
		select memory_id from memory_search
		where memory_search match ?
		order by rank limit ?`, strings.Join(terms, " OR "), limit)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()

	ranked := []rankedIdentifier{}
	for rows.Next() {
		var memoryID string
		if errorValue = rows.Scan(&memoryID); errorValue != nil {
			return nil, errorValue
		}
		ranked = append(ranked, rankedIdentifier{id: memoryID, rank: len(ranked) + 1})
	}
	return ranked, rows.Err()
}

func lexicalTerms(query string) []string {
	terms := []string{}
	for _, field := range strings.Fields(query) {
		trimmed := strings.Trim(field, ".,?!\"'()[]")
		if len([]rune(trimmed)) >= 3 {
			terms = append(terms, `"`+strings.ReplaceAll(trimmed, `"`, ``)+`"`)
		}
	}
	return terms
}

func (store *Store) loadLive(ctx context.Context) (map[string]liveMemory, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select m.memory_id, m.content, m.kind, coalesce(m.valid_until,''),
		       m.resolved_entity_ids, m.unresolved_names,
		       m.storage_strength, m.retrieval_strength, m.created_at, v.vector
		from memory m join memory_vector v using (memory_id)
		where m.superseded_by is null and m.forgotten_at is null`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()

	live := map[string]liveMemory{}
	for rows.Next() {
		var memory Memory
		var resolvedJSON, unresolvedJSON, createdAt string
		var vectorBlob []byte
		if errorValue = rows.Scan(&memory.MemoryID, &memory.Content, &memory.Kind, &memory.ValidUntil,
			&resolvedJSON, &unresolvedJSON, &memory.StorageStrength, &memory.RetrievalStrength,
			&createdAt, &vectorBlob); errorValue != nil {
			return nil, errorValue
		}
		json.Unmarshal([]byte(resolvedJSON), &memory.ResolvedEntityIDs)
		json.Unmarshal([]byte(unresolvedJSON), &memory.UnresolvedNames)
		memory.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		live[memory.MemoryID] = liveMemory{memory: memory, vector: decodeVector(vectorBlob)}
	}
	return live, rows.Err()
}

// A successful recall raises strength, and the rise is largest when the memory
// was hardest to reach. Storage strength only ever increases.
func (store *Store) markRecalled(ctx context.Context, ranked []Ranked) error {
	for _, candidate := range ranked {
		rise := 1.0 / (1.0 + candidate.Memory.RetrievalStrength)
		_, errorValue := store.database.ExecContext(ctx, `
			update memory
			set retrieval_strength = retrieval_strength + ?,
			    storage_strength   = storage_strength + ?,
			    last_recalled_at   = ?
			where memory_id = ?`,
			rise, rise/2, store.now().Format(time.RFC3339), candidate.Memory.MemoryID)
		if errorValue != nil {
			return errorValue
		}
	}
	return nil
}

func (store *Store) bury(ctx context.Context, memory Memory, reason string) error {
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	if _, errorValue = transaction.ExecContext(ctx,
		`insert or replace into tombstone (memory_id, content, kind, reason, died_at) values (?,?,?,?,?)`,
		memory.MemoryID, memory.Content, memory.Kind, reason, store.now().Format(time.RFC3339)); errorValue != nil {
		return errorValue
	}
	if _, errorValue = transaction.ExecContext(ctx,
		`update memory set forgotten_at = ? where memory_id = ?`,
		store.now().Format(time.RFC3339), memory.MemoryID); errorValue != nil {
		return errorValue
	}
	return transaction.Commit()
}

func insertMemory(ctx context.Context, transaction *sql.Tx, memory Memory, vector []float32) error {
	resolvedJSON, _ := json.Marshal(memory.ResolvedEntityIDs)
	unresolvedJSON, _ := json.Marshal(memory.UnresolvedNames)
	validUntil := any(nil)
	if memory.ValidUntil != "" {
		validUntil = memory.ValidUntil
	}
	if _, errorValue := transaction.ExecContext(ctx, `
		insert into memory (memory_id, content, kind, valid_until, resolved_entity_ids,
		                    unresolved_names, storage_strength, retrieval_strength, created_at)
		values (?,?,?,?,?,?,?,?,?)`,
		memory.MemoryID, memory.Content, memory.Kind, validUntil, string(resolvedJSON),
		string(unresolvedJSON), memory.StorageStrength, memory.RetrievalStrength,
		memory.CreatedAt.Format(time.RFC3339)); errorValue != nil {
		return errorValue
	}
	if _, errorValue := transaction.ExecContext(ctx,
		`insert into memory_search (content, memory_id) values (?,?)`,
		memory.Content, memory.MemoryID); errorValue != nil {
		return errorValue
	}
	_, errorValue := transaction.ExecContext(ctx,
		`insert into memory_vector (memory_id, vector) values (?,?)`,
		memory.MemoryID, encodeVector(vector))
	return errorValue
}

func cosineSimilarity(first []float32, second []float32) float64 {
	if len(first) != len(second) {
		return 0
	}
	var dot, firstNorm, secondNorm float64
	for index := range first {
		dot += float64(first[index]) * float64(second[index])
		firstNorm += float64(first[index]) * float64(first[index])
		secondNorm += float64(second[index]) * float64(second[index])
	}
	if firstNorm == 0 || secondNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(firstNorm) * math.Sqrt(secondNorm))
}

func encodeVector(vector []float32) []byte {
	encoded := make([]byte, 4*len(vector))
	for index, value := range vector {
		binary.LittleEndian.PutUint32(encoded[4*index:], math.Float32bits(value))
	}
	return encoded
}

func decodeVector(encoded []byte) []float32 {
	vector := make([]float32, len(encoded)/4)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*index:]))
	}
	return vector
}

func newIdentifier() string {
	identifierBytes := make([]byte, 8)
	rand.Read(identifierBytes)
	return hex.EncodeToString(identifierBytes)
}
