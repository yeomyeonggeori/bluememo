package bluememo_test

import (
	"context"
	"slices"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
)

func TestANameResolvesOnlyWhileExactlyOnePersonAnswersToIt(t *testing.T) {
	registry := &bluememo.PeopleRegistry{People: []bluememo.Person{{PersonID: "person-sample", Names: []string{"이샘플"}}}}
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.People = registry })
	testFixture.settle(t, "소개", statement("이샘플은 결제팀에서 일한다."))
	memory := testFixture.memoryWithContent(t, "이샘플은 결제팀에서 일한다.")
	if !slices.Equal(memory.ResolvedEntityIDs, []string{"person-sample"}) || len(memory.UnresolvedNames) != 0 {
		t.Fatalf("a unique name should resolve, got %+v", memory)
	}

	registry.People = append(registry.People, bluememo.Person{PersonID: "person-namesake", Names: []string{"이샘플"}})
	changedCount, errorValue := testFixture.store.ResolveEntitiesAgain(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	memory = testFixture.memoryWithContent(t, "이샘플은 결제팀에서 일한다.")
	if changedCount != 1 || len(memory.ResolvedEntityIDs) != 0 || !slices.Equal(memory.UnresolvedNames, []string{"이샘플"}) {
		t.Fatalf("a namesake joining should unresolve the name and say so, got %d changed and %+v", changedCount, memory)
	}
	if recalled := recalledContents(testFixture.recall(t, "이샘플 결제팀", 1)); len(recalled) != 1 {
		t.Fatalf("an unresolved name must still be found by its words, got %v", recalled)
	}
}
