package bluememo_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestIngestReplayReturnsCanonicalReceiptWithoutModelCalls(t *testing.T) {
	fixture := newIngestFixture()
	request := fixture.request("이샘플 prefers compact summaries")
	fixture.model.Queue(bluememotest.IngestResponse(bluememotest.IngestFact{Content: "이샘플 prefers compact summaries", Kind: bluememo.FactKindPreference, Relation: bluememo.FactRelationNew}))
	first, errorValue := fixture.ingester.Ingest(context.Background(), request)
	if errorValue != nil || len(first.Facts) != 1 {
		t.Fatalf("initial ingest failed: %+v (%v)", first, errorValue)
	}
	document, errorValue := json.Marshal(first)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	var encoded map[string]json.RawMessage
	if errorValue := json.Unmarshal(document, &encoded); errorValue != nil {
		t.Fatal(errorValue)
	}
	for _, field := range []string{"supersededFactIDs", "reinforcedFactIDs"} {
		if string(encoded[field]) != "[]" {
			t.Fatalf("empty receipt field %s must serialize as an array: %s", field, document)
		}
	}
	request.Episode.EpisodeID = "replayed-attempt"
	replayed, errorValue := fixture.ingester.Ingest(context.Background(), request)
	if errorValue != nil || replayed.EpisodeID != first.EpisodeID || len(replayed.Facts) != 1 || replayed.Facts[0].FactID != first.Facts[0].FactID || fixture.model.RequestCount() != 1 {
		t.Fatalf("replay must return the committed result without a model call: %+v (%v)", replayed, errorValue)
	}
	if _, errorValue := fixture.ingester.Store.Forget(context.Background(), request.Reader, []string{first.Facts[0].FactID}, "requested"); errorValue != nil {
		t.Fatal(errorValue)
	}
	replayed, errorValue = fixture.ingester.Ingest(context.Background(), request)
	if errorValue != nil || len(replayed.Facts) != 0 || fixture.model.RequestCount() != 1 {
		t.Fatalf("replay must not disclose forgotten content: %+v (%v)", replayed, errorValue)
	}
	request.Episode.Content = "different payload"
	if _, errorValue := fixture.ingester.Ingest(context.Background(), request); errorValue == nil || fixture.model.RequestCount() != 1 {
		t.Fatal("conflicting replay must fail before model execution")
	}
}
