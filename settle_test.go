package bluememo_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

func TestSettleInsertsUnrelatedPropositionsAsOneBundle(t *testing.T) {
	testFixture := newFixture(t)
	report := testFixture.settle(t, "배포 절차 설명",
		statement("이샘플은 릴리스 때 admind와 capabilityd를 함께 올린다."),
		statement("이샘플은 하나만 올리면 프로토콜이 어긋난다고 말했다."))

	if report.Inserted != 2 || report.Groups != 1 {
		t.Fatalf("expected one group inserting two memories, got %+v", report)
	}
	memories := testFixture.memories(t)
	if len(memories) != 2 || memories[0].OriginID != memories[1].OriginID {
		t.Fatalf("expected two siblings sharing an origin, got %+v", memories)
	}
}

func TestSameKeepsTheBetterWordingAndSupersedesTheOther(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 3})
	testFixture.settle(t, "첫 말", trait("이샘플은 요약을 짧게 받고 싶어 한다."))
	held := testFixture.memoryWithContent(t, "이샘플은 요약을 짧게 받고 싶어 한다.")

	testFixture.clock.advance(10 * 24 * time.Hour)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationSame, TargetIndex: 0, Importance: 2})
	report := testFixture.settle(t, "다시 말함", statement("이샘플은 짧은 요약을 좋아한다."))
	if report.Reinforced != 1 || len(testFixture.memories(t)) != 1 {
		t.Fatalf("a worse wording of the same thing should only reinforce, got %+v and %v", report, contents(testFixture.memories(t)))
	}
	reinforced := testFixture.memoryWithContent(t, held.Content)
	if reinforced.StorageStrength <= held.StorageStrength {
		t.Fatalf("expected reinforcement, strength stayed %v", reinforced.StorageStrength)
	}

	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationSame, TargetIndex: 0, Importance: 5})
	testFixture.settle(t, "더 나은 말", statement("이샘플은 요약을 세 줄 이내로 받고 싶어 한다."))
	winner := testFixture.memoryWithContent(t, "이샘플은 요약을 세 줄 이내로 받고 싶어 한다.")
	if !winner.IsStatic || winner.StorageStrength < reinforced.StorageStrength {
		t.Fatalf("the better wording should inherit the static flag and strength, got %+v", winner)
	}
	if len(testFixture.memories(t)) != 1 {
		t.Fatalf("the replaced wording should leave retrieval, live: %v", contents(testFixture.memories(t)))
	}
}

func TestUpdatesSupersedesImmediatelyAndExtendsKeepsBoth(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "처음", statement("이샘플은 플랫폼팀에서 일한다."))

	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUpdates, TargetIndex: 0, Importance: 4})
	report := testFixture.settle(t, "이동", statement("이샘플은 결제팀으로 옮겼다."))
	if report.Superseded != 1 || !slices.Equal(contents(testFixture.memories(t)), []string{"이샘플은 결제팀으로 옮겼다."}) {
		t.Fatalf("updates should leave only the new fact live, got %+v and %v", report, contents(testFixture.memories(t)))
	}
	if recalled := recalledContents(testFixture.recall(t, "이샘플은 플랫폼팀에서 일한다.", 5)); slices.Contains(recalled, "이샘플은 플랫폼팀에서 일한다.") {
		t.Fatalf("a superseded fact must not be recalled, got %v", recalled)
	}

	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationExtends, TargetIndex: 0, Importance: 3})
	report = testFixture.settle(t, "세부", statement("이샘플은 결제팀에서 정산을 맡는다."))
	if report.Extended != 1 || len(testFixture.memories(t)) != 2 {
		t.Fatalf("extends should keep both, got %+v and %v", report, contents(testFixture.memories(t)))
	}
}

func TestNoiseNeverEntersAndAnInvalidTargetFallsBackToKeepingApart(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationNoise, TargetIndex: -1, Importance: 1})
	if report := testFixture.settle(t, "ㅋㅋ", statement("이샘플은 웃었다.")); report.Dropped != 1 || len(testFixture.memories(t)) != 0 {
		t.Fatalf("noise should be dropped, got %+v", report)
	}

	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationSame, TargetIndex: 7, Importance: 3})
	if report := testFixture.settle(t, "사실", statement("이샘플은 서울에 산다.")); report.Inserted != 1 {
		t.Fatalf("a judgement naming a candidate that was never offered must insert, got %+v", report)
	}
}

func TestTheRuntimeComputesExpiryFromWhenTheNoteArrived(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "이번 분기만 한국어로", bluememo.Proposition{Content: "이샘플은 한국어로 답을 받고 싶어 한다.", Expiry: bluememo.ExpiryEndOfQuarter})
	memory := testFixture.memoryWithContent(t, "이샘플은 한국어로 답을 받고 싶어 한다.")
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !memory.ValidUntil.Equal(want) {
		t.Fatalf("valid until %v, want %v", memory.ValidUntil, want)
	}

	report := testFixture.settle(t, "지난 일",
		bluememo.Proposition{Content: "이샘플은 휴가 중이다.", Expiry: bluememo.ExpiryOnDate, ExpiryDate: "2026-09-01"},
		bluememo.Proposition{Content: "이샘플은 출장 중이다.", Expiry: bluememo.ExpiryOnDate},
		bluememo.Proposition{Content: "이샘플은 재택 중이다.", Expiry: "sometime"})
	if report.Rejected != 3 {
		t.Fatalf("an expiry in the past, a missing date and an undeclared expiry must be rejected, got %+v", report)
	}
}

func TestAnExplicitNoteStartsStrongerThanAnObservedOne(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.model.QueueDecomposition(statement("이샘플은 금요일에 배포하지 않는다."))
	if errorValue := testFixture.store.Memorize(context.Background(), bluememo.Note{Body: "금요일 배포 금지 기억해", IsExplicit: true}); errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, errorValue := testFixture.store.Settle(context.Background()); errorValue != nil {
		t.Fatal(errorValue)
	}
	testFixture.settle(t, "관찰", statement("이샘플은 아침에 회의한다."))
	explicit := testFixture.memoryWithContent(t, "이샘플은 금요일에 배포하지 않는다.")
	observed := testFixture.memoryWithContent(t, "이샘플은 아침에 회의한다.")
	if explicit.StorageStrength <= observed.StorageStrength {
		t.Fatalf("explicit %v should start above observed %v", explicit.StorageStrength, observed.StorageStrength)
	}
}

func TestAClaimedGroupWaitsForItsLeaseAfterAFailedSettle(t *testing.T) {
	testFixture := newFixture(t)
	if errorValue := testFixture.store.Memorize(context.Background(), bluememo.Note{Body: "이샘플은 서울 사무실에 나온다"}); errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, errorValue := testFixture.store.Settle(context.Background()); errorValue == nil {
		t.Fatal("expected the settle to fail without a decomposition")
	}
	testFixture.model.QueueDecomposition(statement("이샘플은 서울 사무실에 나온다."))
	if report, errorValue := testFixture.store.Settle(context.Background()); errorValue != nil || report.Groups != 0 {
		t.Fatalf("a claimed group should wait for its lease, got %+v (%v)", report, errorValue)
	}
	testFixture.clock.advance(bluememo.DefaultClaimDuration + time.Second)
	if report, errorValue := testFixture.store.Settle(context.Background()); errorValue != nil || report.Inserted != 1 {
		t.Fatalf("an expired claim should be taken again, got %+v (%v)", report, errorValue)
	}
}

func TestUnsettledNotesAnswerLexicallyUntilTheySettle(t *testing.T) {
	testFixture := newFixture(t)
	if errorValue := testFixture.store.Memorize(context.Background(), bluememo.Note{Body: "이샘플은 목요일마다 정산 회의에 들어간다"}); errorValue != nil {
		t.Fatal(errorValue)
	}
	if unsettled := testFixture.recall(t, "정산 회의 언제야", 5).Unsettled; len(unsettled) != 1 {
		t.Fatalf("expected the pending note to answer, got %v", unsettled)
	}
	testFixture.model.QueueDecomposition(statement("이샘플은 목요일마다 정산 회의에 들어간다."))
	if _, errorValue := testFixture.store.Settle(context.Background()); errorValue != nil {
		t.Fatal(errorValue)
	}
	result := testFixture.recall(t, "정산 회의 언제야", 5)
	if len(result.Unsettled) != 0 || len(result.Memories) != 1 {
		t.Fatalf("after settling the memory should answer instead of the note, got %+v", result)
	}
}

func TestRehearsalKeepsOnlyPhrasesThatPickOutTheirOwnMemory(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "배경", statement("사무실 생활 규칙은 팀마다 다르다."))

	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 4})
	testFixture.model.QueueTriggers("아침에만 커피를", "사무실 생활 규칙은 팀마다 다르다")
	report := testFixture.settle(t, "커피", statement("박예시는 아침에만 커피를 마신다."))
	memory := testFixture.memoryWithContent(t, "박예시는 아침에만 커피를 마신다.")
	phrases, errorValue := testFixture.store.TriggerPhrases(context.Background(), memory.MemoryID)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if !slices.Equal(phrases, []string{"아침에만 커피를"}) || report.DroppedTriggerCount != 1 {
		t.Fatalf("expected only the specific phrase, got %v (%+v)", phrases, report)
	}

	requestsBefore := len(testFixture.model.Requests)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "지나가는 말", statement("이샘플은 오늘 우산을 챙겼다."))
	if len(testFixture.model.Requests) != requestsBefore+1 {
		t.Fatalf("a low-importance memory should not be rehearsed, requests went %d → %d", requestsBefore, len(testFixture.model.Requests))
	}
}
