package bluememo_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func contractFile() bluememo.File {
	return bluememo.File{
		FileID:    "k7m2qx9fjh4t8",
		Name:      "2026-03-15-audit-report-fy2025",
		Extension: "pdf",
		Kind:      bluememo.FileKindDocument,
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
	if stored.Kind != bluememo.FileKindDocument || stored.Category != "03" {
		t.Fatalf("kind %q category %q", stored.Kind, stored.Category)
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
		Kind: bluememo.FileKindImage, Summary: "Six people at a desk in the Seoul office.",
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
		bluememo.FileRequest{Text: file.Summary, Category: "13"})
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

func TestAFileOfAnUnknownKindIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	file.Kind = ""
	errorValue := testFixture.store.StoreFile(context.Background(), file)
	if !errors.Is(errorValue, bluememo.ErrUnknownFileKind) {
		t.Fatalf("an empty kind passed as a kind: %v", errorValue)
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
		asked string
		want  int
	}{{"03", 3}, {"031", 2}, {"0312", 1}, {"04", 1}, {"", 4}} {
		recalled, errorValue := testFixture.store.RecallFiles(context.Background(),
			bluememo.FileRequest{Text: "audit", Category: each.asked})
		if errorValue != nil {
			t.Fatalf("recall %q: %v", each.asked, errorValue)
		}
		if len(recalled) != each.want {
			t.Fatalf("category %q reached %d files, wanted %d", each.asked, len(recalled), each.want)
		}
	}
}

func TestACategoryLongerThanItsCodeIsRefused(t *testing.T) {
	testFixture := newFixture(t)
	file := contractFile()
	file.Category = "03-finance"
	errorValue := testFixture.store.StoreFile(context.Background(), file)
	if !errors.Is(errorValue, bluememo.ErrCategoryTooLong) {
		t.Fatalf("a category wider than four characters was stored: %v", errorValue)
	}
}
