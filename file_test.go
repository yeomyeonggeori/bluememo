package bluememo_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func contractFile() bluememo.File {
	return bluememo.File{
		FileID:    "k7m2qx9fjh4t8",
		Name:      "2026-03-15-audit-report-fy2025",
		Extension: "pdf",
		Medium:    bluememo.MediumDocument,
		Summary:   "The FY2025 audit report for 여명거리, signed 2026-03-15.",
		Data:      json.RawMessage(`{"text":"# Audit report\n\nNo material weaknesses."}`),
		Category:  "03",
	}
}

func TestAFileComesBackWithTheTextItCarried(t *testing.T) {
	testFixture := newFixture(t)
	if errorValue := testFixture.store.StoreFile(context.Background(), contractFile()); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	stored, errorValue := testFixture.store.File(context.Background(), "k7m2qx9fjh4t8")
	if errorValue != nil {
		t.Fatalf("read file: %v", errorValue)
	}
	if stored.Medium != bluememo.MediumDocument || stored.Category != "03" {
		t.Fatalf("medium %q category %q", stored.Medium, stored.Category)
	}
	var carried struct{ Text string }
	if errorValue := json.Unmarshal(stored.Data, &carried); errorValue != nil {
		t.Fatalf("data is not the JSON it was given: %v", errorValue)
	}
	if carried.Text == "" {
		t.Fatal("the data lost the text the file carried")
	}
}

func TestAFileIsFoundByWhatItsSummarySays(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	other := bluememo.File{
		FileID: "3n8b4tjhf9xq2", Name: "team-photo", Extension: "jpg",
		Medium: bluememo.MediumImage, Summary: "Six people at a desk in the Seoul office.",
		Data: json.RawMessage(`{"text":"Six people at a desk."}`), Category: "13",
	}
	for _, each := range []bluememo.File{file, other} {
		if errorValue := testFixture.store.StoreFile(context.Background(), each); errorValue != nil {
			t.Fatalf("store file: %v", errorValue)
		}
	}
	recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
		bluememo.FileRequest{Text: file.Summary})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 2 {
		t.Fatalf("recalled %d files, wanted both", len(recalled))
	}
	if recalled[0].File.FileID != file.FileID {
		t.Fatalf("the audit report ranked behind %q", recalled[0].File.FileID)
	}
}

func TestACategoryNarrowsWhichFilesAreSearched(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
		bluememo.FileRequest{Text: file.Summary, Categories: []string{"13"}})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 0 {
		t.Fatalf("a finance file answered a media search: %d", len(recalled))
	}
}

func TestAReplacedFileStopsBeingTheCurrentOne(t *testing.T) {
	testFixture := newFixture(t)
	first := contractFile()
	if errorValue := testFixture.store.StoreFile(context.Background(), first); errorValue != nil {
		t.Fatalf("store first: %v", errorValue)
	}
	second := first
	second.FileID = "9q4hm2xtjb7n3"
	second.Name = "2026-04-02-audit-report-fy2025-revised"
	second.Supersedes = first.FileID
	if errorValue := testFixture.store.StoreFile(context.Background(), second); errorValue != nil {
		t.Fatalf("store second: %v", errorValue)
	}
	recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
		bluememo.FileRequest{Text: first.Summary})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 1 || recalled[0].File.FileID != second.FileID {
		t.Fatalf("recall returned %d files, first is %q", len(recalled), recalled[0].File.FileID)
	}
	if _, errorValue := testFixture.store.File(context.Background(), first.FileID); errorValue != nil {
		t.Fatalf("the replaced file should still be readable by its identifier: %v", errorValue)
	}
}

func TestAFileSurvivesThePressureThatColdsAMemory(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.Capacity = 1 })
	file := contractFile()
	if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	for _, body := range []string{"이샘플은 커피를 마신다.", "박예시는 서울에 산다.", "최견본은 달리기를 한다."} {
		testFixture.settle(t, body, bluememo.Proposition{Content: body})
	}
	if _, errorValue := testFixture.store.Sweep(context.Background()); errorValue != nil {
		t.Fatalf("sweep: %v", errorValue)
	}
	recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
		bluememo.FileRequest{Text: file.Summary})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 1 {
		t.Fatalf("pressure took the file away: %d left", len(recalled))
	}
}

func TestAFileWithoutASummaryIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	file.Summary = ""
	errorValue := testFixture.store.StoreFile(context.Background(), file)
	if !errors.Is(errorValue, bluememo.ErrSummaryMissing) {
		t.Fatalf("stored a file nothing can search for: %v", errorValue)
	}
}

func TestAFileOfAnUnknownMediumIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	file.Medium = ""
	errorValue := testFixture.store.StoreFile(context.Background(), file)
	if !errors.Is(errorValue, bluememo.ErrUnknownMedium) {
		t.Fatalf("an empty medium passed as a medium: %v", errorValue)
	}
}

func TestReembedMovesAFileOntoTheCurrentModel(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	testFixture.store.Close()

	moved, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{
		Embedder: bluememotest.HashEmbedder{}, EmbeddingModel: "hash-v2"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer moved.Close()

	before, errorValue := moved.RecallFiles(context.Background(), bluememo.FileRequest{Text: file.Summary})
	if errorValue != nil {
		t.Fatalf("recall before: %v", errorValue)
	}
	if len(before) != 0 {
		t.Fatal("a file embedded by the old model answered a search under the new one")
	}
	report, errorValue := moved.Reembed(context.Background(), 8)
	if errorValue != nil {
		t.Fatalf("reembed: %v", errorValue)
	}
	if report.Files != 1 {
		t.Fatalf("reembed moved %d files", report.Files)
	}
	after, errorValue := moved.RecallFiles(context.Background(), bluememo.FileRequest{Text: file.Summary})
	if errorValue != nil {
		t.Fatalf("recall after: %v", errorValue)
	}
	if len(after) != 1 {
		t.Fatalf("the file is still unreachable after reembedding: %d", len(after))
	}
}

func TestACategoryCodeReachesEverythingBelowIt(t *testing.T) {
	testFixture := newFixture(t)
	for identifier, category := range map[string]string{
		"aaa1111111111": "03",
		"bbb2222222222": "031",
		"ccc3333333333": "0312",
		"ddd4444444444": "04",
	} {
		file := contractFile()
		file.FileID = identifier
		file.Category = category
		if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
			t.Fatalf("store %s: %v", identifier, errorValue)
		}
	}
	for _, each := range []struct {
		asked []string
		want  int
	}{
		{[]string{"03"}, 3},
		{[]string{"031"}, 2},
		{[]string{"0312"}, 1},
		{[]string{"04"}, 1},
		{nil, 4},
		{[]string{"031", "04"}, 3},
		{[]string{"03", "031"}, 3},
	} {
		recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
			bluememo.FileRequest{Text: "audit", Categories: each.asked})
		if errorValue != nil {
			t.Fatalf("recall %v: %v", each.asked, errorValue)
		}
		if len(recalled) != each.want {
			t.Fatalf("categories %v reached %d files, wanted %d", each.asked, len(recalled), each.want)
		}
	}
}

func TestACategoryThatIsNotAnAsciiCodeIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	for _, refused := range []string{"03-finance", "03-f", "재무", "a b", "aaaaa", "03."} {
		file := contractFile()
		file.Category = refused
		errorValue := testFixture.store.StoreFile(context.Background(), file)
		if !errors.Is(errorValue, bluememo.ErrCategoryCode) {
			t.Fatalf("category %q was stored: %v", refused, errorValue)
		}
	}
	for _, accepted := range []string{"", "0", "a", "03", "a3Z", "0a1B"} {
		file := contractFile()
		file.Category = accepted
		if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
			t.Fatalf("category %q was refused: %v", accepted, errorValue)
		}
	}
}

func TestAFileCarriesItsOccurrenceIntoWhatIsEmbedded(t *testing.T) {
	file := contractFile()
	file.OccurredAt = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	dated := "2026-03-15: " + file.Summary
	embedder := bluememotest.TableEmbedder{Vectors: map[string][]float32{
		dated:        bluememotest.Axes(map[int]float32{1: 1}),
		file.Summary: bluememotest.Axes(map[int]float32{2: 1}),
	}}
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.Embedder = embedder
		configuration.EmbedTimeReference = true
	})
	if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
		bluememo.FileRequest{Text: dated})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 1 || recalled[0].Relevance < 0.99 {
		t.Fatalf("the occurrence did not reach the vector: %+v", recalled)
	}
	if reference := recalled[0].File.TimeReference(time.UTC); reference != "2026-03-15" {
		t.Fatalf("time reference is %q", reference)
	}
}

func TestAFileThatEndsWithoutBeginningIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	file.OccurredUntil = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	errorValue := testFixture.store.StoreFile(context.Background(), file)
	if !errors.Is(errorValue, bluememo.ErrOccurrenceEndsWithoutStart) {
		t.Fatalf("a file ending at a time it never began: %v", errorValue)
	}
}

func TestReembeddingAFileKeepsItsOccurrenceInTheVector(t *testing.T) {
	file := contractFile()
	file.OccurredAt = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	dated := "2026-03-15: " + file.Summary
	embedder := bluememotest.TableEmbedder{Vectors: map[string][]float32{
		dated:        bluememotest.Axes(map[int]float32{1: 1}),
		file.Summary: bluememotest.Axes(map[int]float32{2: 1}),
	}}
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.Embedder = embedder
		configuration.EmbedTimeReference = true
	})
	if errorValue := testFixture.store.StoreFile(context.Background(), file); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	testFixture.store.Close()

	moved, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{
		Embedder: embedder, EmbeddingModel: "table-v2", EmbedTimeReference: true})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer moved.Close()
	if _, errorValue := moved.Reembed(context.Background(), 8); errorValue != nil {
		t.Fatalf("reembed: %v", errorValue)
	}
	recalled, errorValue := moved.RecallFiles(context.Background(), bluememo.FileRequest{Text: dated})
	if errorValue != nil {
		t.Fatalf("recall files: %v", errorValue)
	}
	if len(recalled) != 1 || recalled[0].Relevance < 0.99 {
		t.Fatalf("reembedding dropped the occurrence from the vector: %+v", recalled)
	}
}
