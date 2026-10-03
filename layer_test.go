package bluememo_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

type record []string

func (held record) Search(_ context.Context, _ string, limit int) ([]bluememo.RecalledMemory, error) {
	found := []bluememo.RecalledMemory{}
	for index, content := range held[:min(limit, len(held))] {
		found = append(found, bluememo.RecalledMemory{Memory: bluememo.Memory{MemoryID: "record-" + string(rune('a'+index)), Content: content}})
	}
	return found, nil
}

func TestAStoreOnNothingAsksTheJudgeOncePerStatement(t *testing.T) {
	person := newFixture(t)
	person.settle(t, "처음", statement("이샘플 parks on level 3."))

	if len(person.judge.Seen) != 1 {
		t.Fatalf("a store standing on nothing should judge each statement once, judged %d times", len(person.judge.Seen))
	}
}

func TestAStatementTheLayerBeneathKnowsIsNotWrittenAgain(t *testing.T) {
	company := newFixture(t)
	company.settle(t, "회사 공지", trait("The weekly meeting is on Monday."))
	person := newFixture(t)
	person.store = person.store.On(company.store)

	person.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationSame, TargetIndex: 0, Importance: 3})
	report := person.settle(t, "회의 언제야", statement("The weekly meeting is held on Mondays."))

	if report.Known != 1 || report.Inserted != 0 || len(person.memories(t)) != 0 {
		t.Fatalf("what the company already knows should not enter the person's memory, got %+v and %v", report, contents(person.memories(t)))
	}
	if !slices.Contains(person.judge.Seen[0], "The weekly meeting is on Monday.") {
		t.Fatalf("the judge should have been shown what the layer beneath knows, saw %v", person.judge.Seen[0])
	}
}

func TestAStatementTheLayerBeneathOnlyResemblesIsWritten(t *testing.T) {
	company := newFixture(t)
	company.settle(t, "회사 공지", trait("The weekly meeting is on Monday."))
	person := newFixture(t)
	person.store = person.store.On(company.store)

	person.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 3})
	report := person.settle(t, "회의 불참", statement("이샘플 skips the weekly meeting this month."))

	if report.Known != 0 || report.Inserted != 1 {
		t.Fatalf("a statement the layer beneath does not hold should be written, got %+v", report)
	}
}

func TestARecordThatIsNotAStoreIsALayerToo(t *testing.T) {
	person := newFixture(t)
	person.store = person.store.On(record{"Task 'Quarterly report' was completed on 2026-09-28."})

	person.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationSame, TargetIndex: 0, Importance: 2})
	report := person.settle(t, "보고서 끝냈어", statement("이샘플 completed the quarterly report on 2026-09-28."))

	if report.Known != 1 || len(person.memories(t)) != 0 {
		t.Fatalf("a fact the record holds should not be copied into memory, got %+v", report)
	}
}

func TestRecallReadsThroughTheLayersOwnMemoriesFirst(t *testing.T) {
	company := newFixture(t)
	company.settle(t, "회사 공지", trait("The office closes at 7pm."))
	person := newFixture(t)
	person.settle(t, "내 습관", trait("이샘플 leaves the office at 6pm."))
	person.store = person.store.On(company.store)

	recalled := recalledContents(person.recall(t, "office", 5))

	if !slices.Equal(recalled, []string{"이샘플 leaves the office at 6pm.", "The office closes at 7pm."}) {
		t.Fatalf("expected the person's memory then the company's, got %v", recalled)
	}
}

func TestRecallThroughALayerLeavesTheLayerUnchanged(t *testing.T) {
	company := newFixture(t)
	company.settle(t, "회사 공지", trait("The office closes at 7pm."))
	before := company.memoryWithContent(t, "The office closes at 7pm.")
	person := newFixture(t)
	person.store = person.store.On(company.store)

	person.clock.advance(48 * time.Hour)
	person.recall(t, "office", 5)

	after := company.memoryWithContent(t, "The office closes at 7pm.")
	if after.StorageStrength != before.StorageStrength || !after.LastRecalledAt.Equal(before.LastRecalledAt) {
		t.Fatalf("reading through a layer should not reinforce it, before %+v after %+v", before, after)
	}
}

func TestLayersStack(t *testing.T) {
	company := newFixture(t)
	company.settle(t, "회사 공지", trait("The office closes at 7pm."))
	circle := newFixture(t)
	circle.settle(t, "서클 공지", trait("Leadership meets in the office on Fridays."))
	person := newFixture(t)
	person.store = person.store.On(circle.store.On(company.store))

	recalled := recalledContents(person.recall(t, "office", 5))

	if !slices.Contains(recalled, "The office closes at 7pm.") || !slices.Contains(recalled, "Leadership meets in the office on Fridays.") {
		t.Fatalf("a layer standing on another should read through it, got %v", recalled)
	}
}
