package bluememo_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestAHitBringsTheRestOfItsBundle(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "릴리스 절차",
		statement("릴리스는 admind와 capabilityd를 함께 올린다."),
		statement("하나만 올리면 프로토콜이 어긋난다."),
		statement("올린 뒤 버전을 확인한다."))
	testFixture.settle(t, "다른 이야기", statement("박예시는 아침에만 커피를 마신다."))

	result := testFixture.recall(t, "릴리스는 admind와 capabilityd를 함께 올린다", 3)
	if len(result.Memories) != 3 || !result.Memories[1].IsSibling || !result.Memories[2].IsSibling {
		t.Fatalf("expected the leading hit and its two siblings, got %v", recalledContents(result))
	}
}

func TestTwoSyllableWordsReachAMemoryLexically(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "계약", statement("여명거리와 고객사의 계약은 2026-01-01부터 유효하다."))
	testFixture.settle(t, "회의", statement("이샘플은 어제 최견본과 회의했다."))

	lexical, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{EmbeddingModel: "hash"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer lexical.Close()
	result, errorValue := lexical.Recall(context.Background(), "계약 언제부터", 1)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if result.Mode != bluememo.SearchModeLexical || !slices.Equal(recalledContents(result), []string{"여명거리와 고객사의 계약은 2026-01-01부터 유효하다."}) {
		t.Fatalf("expected a lexical answer from two-syllable words, got %+v", result)
	}
}

func TestAFailingEmbedderDegradesToLexicalAndSaysWhy(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.Embedder = bluememotest.HashEmbedder{Failure: errors.New("embedding service down")}
	})
	result := testFixture.recall(t, "아무거나", 5)
	if result.Mode != bluememo.SearchModeLexical || result.DegradedReason == "" {
		t.Fatalf("expected a lexical answer that says why, got %+v", result)
	}
}

func TestATriggerPhraseReachesAMemoryThatSharesNoWordWithTheQuestion(t *testing.T) {
	const contract = "여명거리와 고객사의 계약은 2026-01-01부터 유효하다."
	const office = "이샘플은 서울 사무실에 나온다."
	const phrase = "세금계산서 발행 시점"
	const question = "세금계산서는 언제 끊어도 돼?"
	embedder := bluememotest.TableEmbedder{Vectors: map[string][]float32{
		contract: bluememotest.Axes(map[int]float32{1: 1}),
		office:   bluememotest.Axes(map[int]float32{2: 1}),
		phrase:   bluememotest.Axes(map[int]float32{1: 1, 3: 1}),
		question: bluememotest.Axes(map[int]float32{3: 1}),
	}}
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Embedder = embedder })
	testFixture.settle(t, "배경", statement(office))
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 4})
	testFixture.model.QueueTriggers(phrase)
	testFixture.settle(t, "계약", statement(contract))

	result := testFixture.recall(t, question, 2)
	if len(result.Memories) == 0 || result.Memories[0].Memory.Content != contract || result.Memories[0].TriggerRank != 1 {
		t.Fatalf("expected the contract first through its trigger, got %+v", result.Memories)
	}
}

func TestAnExpiredMemoryIsNotRecalledEvenBeforeTheSweep(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "오늘만", bluememo.Proposition{Content: "이샘플은 오늘 재택근무를 한다.", Expiry: bluememo.ExpiryEndOfToday})
	if recalled := recalledContents(testFixture.recall(t, "이샘플은 오늘 재택근무를 한다.", 5)); len(recalled) != 1 {
		t.Fatalf("expected the memory while it holds, got %v", recalled)
	}
	testFixture.clock.advance(24 * time.Hour)
	if recalled := recalledContents(testFixture.recall(t, "이샘플은 오늘 재택근무를 한다.", 5)); len(recalled) != 0 {
		t.Fatalf("an expired memory must not be recalled, got %v", recalled)
	}
}

func TestColdRecallRevivesAndReinforcesMoreThanWarmRecall(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 1 })
	testFixture.settle(t, "첫째", statement("박예시는 아침에만 커피를 마신다."))
	testFixture.clock.advance(time.Hour)
	testFixture.settle(t, "둘째", statement("최견본은 금요일에 배포하지 않는다."))
	testFixture.clock.advance(10 * 24 * time.Hour)
	testFixture.recall(t, "최견본은 금요일에 배포하지 않는다", 1)
	if _, errorValue := testFixture.store.Sweep(context.Background()); errorValue != nil {
		t.Fatal(errorValue)
	}
	if live := contents(testFixture.memories(t)); !slices.Equal(live, []string{"최견본은 금요일에 배포하지 않는다."}) {
		t.Fatalf("expected the unrecalled memory to go cold, live: %v", live)
	}

	warm := testFixture.memoryWithContent(t, "최견본은 금요일에 배포하지 않는다.")
	testFixture.clock.advance(10 * 24 * time.Hour)
	testFixture.recall(t, "최견본은 금요일에 배포하지 않는다", 1)
	warmGain := testFixture.memoryWithContent(t, warm.Content).StorageStrength - warm.StorageStrength

	result := testFixture.recall(t, "박예시는 아침에만 커피를 마신다", 1)
	if len(result.Memories) != 1 || !result.Memories[0].Memory.IsCold() {
		t.Fatalf("expected the cold memory to answer, got %+v", result.Memories)
	}
	revived := testFixture.memoryWithContent(t, "박예시는 아침에만 커피를 마신다.")
	coldGain := revived.StorageStrength - result.Memories[0].Memory.StorageStrength
	if revived.IsCold() || coldGain <= warmGain || coldGain != bluememo.MaximumRecallReinforcing {
		t.Fatalf("cold recall should revive with the maximum gain; cold %v, warm %v", coldGain, warmGain)
	}
}

func TestProfileHoldsOnlyLiveStaticMemories(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "소개", trait("이샘플은 여명거리의 CTO이다."), statement("이샘플은 어제 회의했다."))
	profile, errorValue := testFixture.store.Profile(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if !slices.Equal(contents(profile), []string{"이샘플은 여명거리의 CTO이다."}) {
		t.Fatalf("expected only the static trait, got %v", contents(profile))
	}
}
