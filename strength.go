package bluememo

import (
	"math"
	"time"
)

const (
	DefaultHalfLife          = 30 * 24 * time.Hour
	InitialStorageStrength   = 1.0
	ExplicitStorageStrength  = 2.0
	MaximumRecallReinforcing = 1.0
)

func Retrievability(memory Memory, halfLife time.Duration, now time.Time) float64 {
	elapsed := now.Sub(lastTouched(memory))
	if elapsed <= 0 {
		return 1
	}
	stability := float64(halfLife) * memory.StorageStrength
	return math.Exp(-math.Ln2 * float64(elapsed) / stability)
}

func RecallReinforcement(memory Memory, halfLife time.Duration, now time.Time) float64 {
	if memory.ColdReason == ColdReasonPressure {
		return MaximumRecallReinforcing
	}
	return MaximumRecallReinforcing - Retrievability(memory, halfLife, now)
}

func Usefulness(memory Memory, halfLife time.Duration, now time.Time) float64 {
	return memory.StorageStrength * Retrievability(memory, halfLife, now)
}

func lastTouched(memory Memory) time.Time {
	if memory.LastRecalledAt.After(memory.CreatedAt) {
		return memory.LastRecalledAt
	}
	return memory.CreatedAt
}
