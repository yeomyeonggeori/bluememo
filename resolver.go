package bluememo

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

type Person struct {
	PersonID string
	Names    []string
}

type PeopleRegistry struct {
	People []Person
}

func (registry PeopleRegistry) Resolve(content string) ([]string, []string) {
	personIDsByName := map[string][]string{}
	for _, person := range registry.People {
		for _, name := range person.Names {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" || !strings.Contains(content, trimmed) || slices.Contains(personIDsByName[trimmed], person.PersonID) {
				continue
			}
			personIDsByName[trimmed] = append(personIDsByName[trimmed], person.PersonID)
		}
	}
	resolved, unresolved := []string{}, []string{}
	for name, personIDs := range personIDsByName {
		if len(personIDs) > 1 {
			unresolved = append(unresolved, name)
			continue
		}
		if !slices.Contains(resolved, personIDs[0]) {
			resolved = append(resolved, personIDs[0])
		}
	}
	slices.Sort(resolved)
	slices.Sort(unresolved)
	return resolved, unresolved
}

func (store *Store) ResolveEntitiesAgain(ctx context.Context) (int, error) {
	memories, errorValue := queryMemories(ctx, store.database, ``)
	if errorValue != nil {
		return 0, errorValue
	}
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return 0, errorValue
	}
	defer transaction.Rollback()
	changedCount := 0
	for _, memory := range memories {
		resolved, unresolved := store.resolveEntities(memory.Content)
		if slices.Equal(resolved, memory.ResolvedEntityIDs) && slices.Equal(unresolved, memory.UnresolvedNames) {
			continue
		}
		resolvedJSON, errorValue := json.Marshal(resolved)
		if errorValue != nil {
			return 0, errorValue
		}
		unresolvedJSON, errorValue := json.Marshal(unresolved)
		if errorValue != nil {
			return 0, errorValue
		}
		if _, errorValue := transaction.ExecContext(ctx, `update memory set resolved_entity_ids = ?, unresolved_names = ? where memory_id = ?`,
			string(resolvedJSON), string(unresolvedJSON), memory.MemoryID); errorValue != nil {
			return 0, errorValue
		}
		changedCount++
	}
	return changedCount, transaction.Commit()
}
