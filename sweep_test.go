package bluememo_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

func sweep(t *testing.T, testFixture *fixture) bluememo.SweepReport {
	t.Helper()
	report, errorValue := testFixture.store.Sweep(context.Background())
	if errorValue != nil {
		t.Fatalf("sweep: %v", errorValue)
	}
	return report
}

func TestASweepWithoutPressureDeletesNothing(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 10 })
	testFixture.settle(t, "처음", statement("이샘플은 플랫폼팀에서 일한다."))
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUpdates, TargetIndex: 0, Importance: 3})
	testFixture.settle(t, "이동", statement("이샘플은 결제팀으로 옮겼다."))
	testFixture.settle(t, "오늘만", bluememo.Proposition{Content: "이샘플은 오늘 재택근무를 한다.", Expiry: bluememo.ExpiryEndOfToday})
	testFixture.clock.advance(400 * 24 * time.Hour)

	report := sweep(t, testFixture)
	tombstones, _ := testFixture.store.Tombstones(context.Background())
	if report.Deleted != 0 || len(tombstones) != 0 || report.Expired != 1 {
		t.Fatalf("with headroom nothing should die, however old or cold; got %+v", report)
	}
}

func TestStaticOverQuotaIsDemotedNotDeleted(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 10 })
	for index := range 3 {
		testFixture.settle(t, "특성", trait(fmt.Sprintf("이샘플의 특성 %d번은 변하지 않는다.", index)))
		testFixture.clock.advance(time.Hour)
	}
	testFixture.recall(t, "이샘플의 특성 2번은 변하지 않는다", 1)

	report := sweep(t, testFixture)
	profile, _ := testFixture.store.Profile(context.Background())
	if report.Demoted != 1 || len(profile) != 2 || len(testFixture.memories(t)) != 3 {
		t.Fatalf("a quota of two should demote one static memory and keep all three, got %+v, profile %v", report, contents(profile))
	}
	if !slices.Contains(contents(profile), "이샘플의 특성 2번은 변하지 않는다.") {
		t.Fatalf("the recalled trait should keep its place, profile %v", contents(profile))
	}
}

func TestPressureCoolsWholeBundlesLeastUsefulFirst(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 3 })
	testFixture.settle(t, "절차", statement("릴리스는 admind와 capabilityd를 함께 올린다."), statement("하나만 올리면 프로토콜이 어긋난다."))
	testFixture.clock.advance(24 * time.Hour)
	testFixture.settle(t, "취향", statement("박예시는 아침에만 커피를 마신다."), statement("박예시는 매운 음식을 못 먹는다."))
	testFixture.clock.advance(24 * time.Hour)
	testFixture.recall(t, "하나만 올리면 프로토콜이 어긋난다", 1)

	report := sweep(t, testFixture)
	live := contents(testFixture.memories(t))
	if report.Cooled != 2 || len(live) != 2 || !slices.Contains(live, "릴리스는 admind와 capabilityd를 함께 올린다.") {
		t.Fatalf("the unrecalled bundle should go cold together, got %+v, live %v", report, live)
	}
}

func TestColdMemoriesDieOnlyAfterTheirGraceAndSupersededFirst(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 2 })
	testFixture.settle(t, "처음", statement("이샘플은 플랫폼팀에서 일한다."))
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUpdates, TargetIndex: 0, Importance: 3})
	testFixture.settle(t, "이동", statement("이샘플은 결제팀으로 옮겼다."))
	testFixture.settle(t, "취향", statement("박예시는 아침에만 커피를 마신다."))

	if report := sweep(t, testFixture); report.Deleted != 0 {
		t.Fatalf("a superseded memory inside its grace must survive, got %+v", report)
	}
	testFixture.clock.advance(bluememo.DefaultColdGrace + time.Hour)
	report := sweep(t, testFixture)
	tombstones, _ := testFixture.store.Tombstones(context.Background())
	if report.Deleted != 1 || len(tombstones) != 1 || tombstones[0].Content != "이샘플은 플랫폼팀에서 일한다." ||
		tombstones[0].Reason != bluememo.TombstoneReason(bluememo.ColdReasonSuperseded) {
		t.Fatalf("the superseded memory should die first once its grace passes, got %+v and %+v", report, tombstones)
	}
}

func TestTombstonesHaveTheirOwnCap(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.TombstoneCapacity = 1 })
	testFixture.settle(t, "둘", statement("박예시는 아침에만 커피를 마신다."), statement("최견본은 금요일에 배포하지 않는다."))
	for _, target := range []string{"커피", "금요일"} {
		if _, errorValue := testFixture.store.Forget(context.Background(), target); errorValue != nil {
			t.Fatal(errorValue)
		}
		testFixture.clock.advance(time.Minute)
	}
	report := sweep(t, testFixture)
	tombstones, _ := testFixture.store.Tombstones(context.Background())
	if report.TombstonesPruned != 1 || len(tombstones) != 1 || tombstones[0].RequestPhrase != "금요일" {
		t.Fatalf("only the newest tombstone should remain, got %+v and %+v", report, tombstones)
	}
}
