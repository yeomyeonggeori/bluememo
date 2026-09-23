package bluememo_test

import (
	"context"
	"slices"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
)

func TestForgetResolvesAUniqueReferenceAndLeavesATombstone(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "취향", statement("박예시는 아침에만 커피를 마신다."), statement("박예시는 매운 음식을 못 먹는다."))

	outcome, errorValue := testFixture.store.Forget(context.Background(), "커피")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if !slices.Equal(contents(outcome.Forgotten), []string{"박예시는 아침에만 커피를 마신다."}) {
		t.Fatalf("a reference only one memory contains should forget it, got %+v", outcome)
	}
	tombstones, errorValue := testFixture.store.Tombstones(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(tombstones) != 1 || tombstones[0].Content != "박예시는 아침에만 커피를 마신다." ||
		tombstones[0].Reason != bluememo.TombstoneReasonAsked || tombstones[0].RequestPhrase != "커피" {
		t.Fatalf("the tombstone should reassemble the memory and say who asked, got %+v", tombstones)
	}
}

func TestForgetNeverDeletesWhatItCouldNotPinDown(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.settle(t, "배포", statement("배포는 금요일에 하지 않는다."), statement("배포 전에 버전을 확인한다."))

	ambiguous, errorValue := testFixture.store.Forget(context.Background(), "배포")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(ambiguous.Forgotten) != 0 || len(ambiguous.Candidates) != 2 {
		t.Fatalf("a reference two memories contain should ask, got %+v", ambiguous)
	}

	unmatched, errorValue := testFixture.store.Forget(context.Background(), "금요일 릴리스 규칙 지워줘")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(unmatched.Forgotten) != 0 || len(unmatched.Candidates) == 0 || len(testFixture.memories(t)) != 2 {
		t.Fatalf("a reference nothing contains should offer candidates and delete nothing, got %+v", unmatched)
	}

	forgotten, errorValue := testFixture.store.ForgetMemories(context.Background(), []string{ambiguous.Candidates[0].MemoryID, ambiguous.Candidates[1].MemoryID}, "배포 얘기 다 지워줘")
	if errorValue != nil || len(forgotten) != 2 || len(testFixture.memories(t)) != 0 {
		t.Fatalf("confirmed identifiers should all be forgotten, got %v (%v)", contents(forgotten), errorValue)
	}
}
