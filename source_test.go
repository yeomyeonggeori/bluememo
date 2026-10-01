package bluememo_test

import (
	"context"
	"testing"

	bluememo "github.com/yeomyeonggeori/bluememo"
)

func settleTwoFactsFromOneNote(t *testing.T, recallSources bool) *fixture {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.RecallSources = recallSources
	})
	for range 2 {
		testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	}
	testFixture.settle(t, "어제 박예시와 정산 회의를 했고 출장비 기준을 올리기로 했다.",
		bluememo.Proposition{Content: "박예시와 정산 회의를 했다.", Expiry: bluememo.ExpiryNone},
		bluememo.Proposition{Content: "출장비 기준을 올리기로 했다.", Expiry: bluememo.ExpiryNone})
	return testFixture
}

func TestRecallCarriesWhatWasActuallySaidOncePerNote(t *testing.T) {
	testFixture := settleTwoFactsFromOneNote(t, true)
	result, errorValue := testFixture.store.Recall(context.Background(), "정산 회의", 10)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(result.Memories) < 2 {
		t.Fatalf("expected both facts, got %d", len(result.Memories))
	}
	if len(result.Sources) != 1 {
		t.Fatalf("two facts share one note, so one source is expected, got %d", len(result.Sources))
	}
	if want := "어제 박예시와 정산 회의를 했고 출장비 기준을 올리기로 했다."; result.Sources[0].Body != want {
		t.Fatalf("source body %q, want %q", result.Sources[0].Body, want)
	}
	if result.Sources[0].OriginID != result.Memories[0].Memory.OriginID {
		t.Fatal("the source must be keyed by the origin its memories carry")
	}
}

func TestRecallLeavesTheSourceOutUnlessItIsAskedFor(t *testing.T) {
	testFixture := settleTwoFactsFromOneNote(t, false)
	result, errorValue := testFixture.store.Recall(context.Background(), "정산 회의", 10)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("sources must stay out by default, got %d", len(result.Sources))
	}
}
