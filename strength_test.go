package bluememo_test

import (
	"math"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

func TestRetrievabilityHalvesOverAHalfLifeScaledByStorageStrength(t *testing.T) {
	createdAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	weak := bluememo.Memory{StorageStrength: 1, CreatedAt: createdAt}
	strong := bluememo.Memory{StorageStrength: 2, CreatedAt: createdAt}
	later := createdAt.Add(bluememo.DefaultHalfLife)

	if value := bluememo.Retrievability(weak, bluememo.DefaultHalfLife, later); math.Abs(value-0.5) > 1e-9 {
		t.Fatalf("retrievability after one half-life is %v, want 0.5", value)
	}
	if value := bluememo.Retrievability(strong, bluememo.DefaultHalfLife, later); math.Abs(value-math.Sqrt(0.5)) > 1e-9 {
		t.Fatalf("a twice-stored memory should fade half as fast, got %v", value)
	}
	if gain := bluememo.RecallReinforcement(weak, bluememo.DefaultHalfLife, createdAt); gain != 0 {
		t.Fatalf("recalling what was just learned should add nothing, got %v", gain)
	}
	if gain := bluememo.RecallReinforcement(weak, bluememo.DefaultHalfLife, later); math.Abs(gain-0.5) > 1e-9 {
		t.Fatalf("recalling a half-faded memory should add half, got %v", gain)
	}
}

func TestExpiryInstantEndsThePeriodTheModelNamed(t *testing.T) {
	seoul := time.FixedZone("KST", 9*3600)
	arrivedAt := time.Date(2026, 12, 30, 23, 30, 0, 0, time.UTC)
	cases := map[bluememo.Expiry]time.Time{
		bluememo.ExpiryEndOfToday:   time.Date(2027, 1, 1, 0, 0, 0, 0, seoul),
		bluememo.ExpiryEndOfWeek:    time.Date(2027, 1, 4, 0, 0, 0, 0, seoul),
		bluememo.ExpiryEndOfMonth:   time.Date(2027, 1, 1, 0, 0, 0, 0, seoul),
		bluememo.ExpiryEndOfQuarter: time.Date(2027, 1, 1, 0, 0, 0, 0, seoul),
		bluememo.ExpiryEndOfYear:    time.Date(2027, 1, 1, 0, 0, 0, 0, seoul),
	}
	for expiry, want := range cases {
		got, errorValue := bluememo.ExpiryInstant(expiry, "", arrivedAt, seoul)
		if errorValue != nil || !got.Equal(want) {
			t.Errorf("%s from %v in Seoul: got %v (%v), want %v", expiry, arrivedAt, got, errorValue, want)
		}
	}
	onDate, errorValue := bluememo.ExpiryInstant(bluememo.ExpiryOnDate, "2027-01-05", arrivedAt, seoul)
	if errorValue != nil || !onDate.Equal(time.Date(2027, 1, 6, 0, 0, 0, 0, seoul)) {
		t.Errorf("on_date should hold through the named day, got %v (%v)", onDate, errorValue)
	}
}
