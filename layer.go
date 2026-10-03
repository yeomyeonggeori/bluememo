package bluememo

import (
	"context"
	"slices"
	"strings"
)

// Known is a layer a store can stand on: anything that answers what it
// already knows about a query, without being changed by the asking.
type Known interface {
	Search(ctx context.Context, query string, limit int) ([]RecalledMemory, error)
}

// On stands the store on the layers beneath it. A statement one of them
// already knows is not written again, and recall reads through all of them,
// the store's own memories first. The store underneath is shared: closing
// either closes both.
func (store *Store) On(beneath ...Known) *Store {
	layered := *store
	layered.beneath = append(slices.Clone(store.beneath), beneath...)
	return &layered
}

func (store *Store) Search(ctx context.Context, query string, limit int) ([]RecalledMemory, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, ErrEmptyQuery
	}
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	embedding, _ := store.embedQuery(ctx, trimmed)
	ranked, _, errorValue := store.search(ctx, searchQuery{text: trimmed, embedding: embedding, limit: limit, now: store.now()})
	if errorValue != nil {
		return nil, errorValue
	}
	ranked, _ = store.rerank(ctx, trimmed, ranked, limit)
	return store.throughLayers(ctx, trimmed, ranked, limit)
}

func (store *Store) throughLayers(ctx context.Context, query string, own []RecalledMemory, limit int) ([]RecalledMemory, error) {
	layers := [][]RecalledMemory{own}
	for _, layer := range store.beneath {
		found, errorValue := layer.Search(ctx, query, limit)
		if errorValue != nil {
			return nil, errorValue
		}
		layers = append(layers, found)
	}
	return interleaved(layers, limit), nil
}

func (store *Store) isKnownBeneath(ctx context.Context, content string) (bool, error) {
	if len(store.beneath) == 0 {
		return false, nil
	}
	known, errorValue := store.throughLayers(ctx, content, nil, SettleCandidateLimit)
	if errorValue != nil || len(known) == 0 {
		return false, errorValue
	}
	contents := make([]string, len(known))
	for index, entry := range known {
		contents[index] = entry.Memory.Content
	}
	judgement, errorValue := store.configuration.Judge.Judge(ctx, content, contents)
	if errorValue != nil {
		return false, errorValue
	}
	return judgement.Relation == RelationSame && judgement.TargetIndex >= 0 && judgement.TargetIndex < len(contents), nil
}

func interleaved(layers [][]RecalledMemory, limit int) []RecalledMemory {
	merged := []RecalledMemory{}
	seen := map[string]bool{}
	for rank := 0; len(merged) < limit; rank++ {
		hasMore := false
		for _, layer := range layers {
			if rank >= len(layer) {
				continue
			}
			hasMore = true
			entry := layer[rank]
			if seen[entry.Memory.MemoryID] || len(merged) >= limit {
				continue
			}
			seen[entry.Memory.MemoryID] = true
			merged = append(merged, entry)
		}
		if !hasMore {
			break
		}
	}
	return merged
}
