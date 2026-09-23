package bluememo

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"time"
)

type SweepReport struct {
	Expired          int `json:"expired"`
	Demoted          int `json:"demoted"`
	Cooled           int `json:"cooled"`
	Deleted          int `json:"deleted"`
	TombstonesPruned int `json:"tombstonesPruned"`
}

var coldDeletionOrder = map[ColdReason]int{ColdReasonSuperseded: 0, ColdReasonExpired: 1, ColdReasonPressure: 2}

func (store *Store) Sweep(ctx context.Context) (SweepReport, error) {
	now := store.now()
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return SweepReport{}, errorValue
	}
	defer transaction.Rollback()
	report := SweepReport{}
	steps := []func(context.Context, *sql.Tx, time.Time, *SweepReport) error{
		store.coolExpired,
		store.demoteStaticOverQuota,
		store.coolLeastUsefulBundles,
		store.deleteColdPastGrace,
		store.pruneTombstones,
	}
	for _, step := range steps {
		if errorValue := step(ctx, transaction, now, &report); errorValue != nil {
			return SweepReport{}, errorValue
		}
	}
	return report, transaction.Commit()
}

func (store *Store) coolExpired(ctx context.Context, transaction *sql.Tx, now time.Time, report *SweepReport) error {
	result, errorValue := transaction.ExecContext(ctx, `
		update memory set cold_since = ?, cold_reason = ?
		where cold_since is null and valid_until is not null and valid_until <= ?`,
		toMilliseconds(now), ColdReasonExpired, toMilliseconds(now))
	if errorValue != nil {
		return errorValue
	}
	report.Expired, errorValue = affectedCount(result)
	return errorValue
}

func (store *Store) demoteStaticOverQuota(ctx context.Context, transaction *sql.Tx, now time.Time, report *SweepReport) error {
	statics, errorValue := queryMemories(ctx, transaction, `where cold_since is null and is_static = 1`)
	if errorValue != nil {
		return errorValue
	}
	excess := len(statics) - store.staticQuota()
	if excess <= 0 {
		return nil
	}
	store.sortLeastUsefulFirst(statics, now)
	for _, memory := range statics[:excess] {
		if _, errorValue := transaction.ExecContext(ctx, `update memory set is_static = 0 where memory_id = ?`, memory.MemoryID); errorValue != nil {
			return errorValue
		}
	}
	report.Demoted = excess
	return nil
}

func (store *Store) coolLeastUsefulBundles(ctx context.Context, transaction *sql.Tx, now time.Time, report *SweepReport) error {
	live, errorValue := queryMemories(ctx, transaction, `where cold_since is null`)
	if errorValue != nil {
		return errorValue
	}
	excess := len(live) - store.configuration.Capacity
	for _, bundle := range store.evictableBundles(live, now) {
		if excess <= 0 {
			return nil
		}
		for _, memory := range bundle {
			if _, errorValue := transaction.ExecContext(ctx, `update memory set cold_since = ?, cold_reason = ? where memory_id = ?`,
				toMilliseconds(now), ColdReasonPressure, memory.MemoryID); errorValue != nil {
				return errorValue
			}
		}
		excess -= len(bundle)
		report.Cooled += len(bundle)
	}
	return nil
}

func (store *Store) evictableBundles(live []Memory, now time.Time) [][]Memory {
	bundlesByOrigin := map[string][]Memory{}
	for _, memory := range live {
		bundlesByOrigin[memory.OriginID] = append(bundlesByOrigin[memory.OriginID], memory)
	}
	bundles := [][]Memory{}
	for _, bundle := range bundlesByOrigin {
		if !containsStatic(bundle) {
			bundles = append(bundles, bundle)
		}
	}
	sort.Slice(bundles, func(left int, right int) bool {
		leftUsefulness, rightUsefulness := store.bundleUsefulness(bundles[left], now), store.bundleUsefulness(bundles[right], now)
		if leftUsefulness != rightUsefulness {
			return leftUsefulness < rightUsefulness
		}
		return bundles[left][0].OriginID < bundles[right][0].OriginID
	})
	return bundles
}

func (store *Store) deleteColdPastGrace(ctx context.Context, transaction *sql.Tx, now time.Time, report *SweepReport) error {
	var storedCount int
	if errorValue := transaction.QueryRowContext(ctx, `select count(*) from memory`).Scan(&storedCount); errorValue != nil {
		return errorValue
	}
	excess := storedCount - store.configuration.Capacity
	if excess <= 0 {
		return nil
	}
	expiredGrace := toMilliseconds(now.Add(-store.configuration.ColdGrace))
	cold, errorValue := queryMemories(ctx, transaction, `where cold_since is not null and cold_since <= ?`, expiredGrace)
	if errorValue != nil {
		return errorValue
	}
	sort.SliceStable(cold, func(left int, right int) bool {
		if coldDeletionOrder[cold[left].ColdReason] != coldDeletionOrder[cold[right].ColdReason] {
			return coldDeletionOrder[cold[left].ColdReason] < coldDeletionOrder[cold[right].ColdReason]
		}
		return Usefulness(cold[left], store.configuration.HalfLife, now) < Usefulness(cold[right], store.configuration.HalfLife, now)
	})
	for _, memory := range cold[:min(excess, len(cold))] {
		if errorValue := bury(ctx, transaction, memory, TombstoneReason(memory.ColdReason), "", now); errorValue != nil {
			return errorValue
		}
		report.Deleted++
	}
	return nil
}

func (store *Store) pruneTombstones(ctx context.Context, transaction *sql.Tx, _ time.Time, report *SweepReport) error {
	result, errorValue := transaction.ExecContext(ctx, `
		delete from tombstone where memory_id in (
			select memory_id from tombstone order by died_at desc, memory_id limit -1 offset ?)`,
		store.configuration.TombstoneCapacity)
	if errorValue != nil {
		return errorValue
	}
	report.TombstonesPruned, errorValue = affectedCount(result)
	return errorValue
}

func (store *Store) staticQuota() int {
	return int(math.Floor(store.configuration.StaticShare * float64(store.configuration.Capacity)))
}

func (store *Store) bundleUsefulness(bundle []Memory, now time.Time) float64 {
	best := 0.0
	for _, memory := range bundle {
		best = max(best, Usefulness(memory, store.configuration.HalfLife, now))
	}
	return best
}

func (store *Store) sortLeastUsefulFirst(memories []Memory, now time.Time) {
	sort.SliceStable(memories, func(left int, right int) bool {
		return Usefulness(memories[left], store.configuration.HalfLife, now) < Usefulness(memories[right], store.configuration.HalfLife, now)
	})
}

func containsStatic(bundle []Memory) bool {
	for _, memory := range bundle {
		if memory.IsStatic {
			return true
		}
	}
	return false
}

func affectedCount(result sql.Result) (int, error) {
	count, errorValue := result.RowsAffected()
	return int(count), errorValue
}
