package bluememo_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
)

func TestANewStoreIsEmpty(t *testing.T) {
	isEmpty, errorValue := newFixture(t).store.IsEmpty(context.Background())
	if errorValue != nil || !isEmpty {
		t.Fatalf("a store nothing was written to: empty = %v, error = %v", isEmpty, errorValue)
	}
}

func TestANoteWaitingToBeSettledIsNotEmpty(t *testing.T) {
	testFixture := newFixture(t)
	if errorValue := testFixture.store.Memorize(context.Background(), bluememo.Note{GroupID: "group-1", Body: "박예시 keeps the ledger."}); errorValue != nil {
		t.Fatal(errorValue)
	}

	isEmpty, errorValue := testFixture.store.IsEmpty(context.Background())
	if errorValue != nil || isEmpty {
		t.Fatalf("a store holding an unsettled note: empty = %v, error = %v; it would be lost", isEmpty, errorValue)
	}
}

func TestAnAdoptedMemoryIsNotEmpty(t *testing.T) {
	testFixture := newFixture(t)
	if _, errorValue := testFixture.store.Adopt(context.Background(), []bluememo.AdoptedMemory{adoptedFact("carried-1", "이샘플 leads the spring audit.")}); errorValue != nil {
		t.Fatal(errorValue)
	}

	isEmpty, errorValue := testFixture.store.IsEmpty(context.Background())
	if errorValue != nil || isEmpty {
		t.Fatalf("a store holding a memory: empty = %v, error = %v", isEmpty, errorValue)
	}
}
