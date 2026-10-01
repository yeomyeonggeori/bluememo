package bluememo_test

import (
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

func TestAnOccurrenceCoversExactlyWhatItsPrecisionSays(t *testing.T) {
	seoul, errorValue := time.LoadLocation("Asia/Seoul")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	for _, each := range []struct {
		written string
		first   string
		last    string
	}{
		{"2026", "2026-01-01T00:00", "2026-12-31T23:59"},
		{"2026-H1", "2026-01-01T00:00", "2026-06-30T23:59"},
		{"2026-H2", "2026-07-01T00:00", "2026-12-31T23:59"},
		{"2026-Q1", "2026-01-01T00:00", "2026-03-31T23:59"},
		{"2026-Q3", "2026-07-01T00:00", "2026-09-30T23:59"},
		{"2026-03", "2026-03-01T00:00", "2026-03-31T23:59"},
		{"2026-03-05", "2026-03-05T00:00", "2026-03-05T23:59"},
		{"2026-03-05T15:30", "2026-03-05T15:30", "2026-03-05T15:30"},
		{"2026-Q1/2026-Q3", "2026-01-01T00:00", "2026-09-30T23:59"},
		{"2026-03/2026-05", "2026-03-01T00:00", "2026-05-31T23:59"},
	} {
		occurrence, errorValue := bluememo.ParseOccurrence(each.written)
		if errorValue != nil {
			t.Fatalf("%s: %v", each.written, errorValue)
		}
		first, last, errorValue := occurrence.Span(seoul)
		if errorValue != nil {
			t.Fatalf("%s: %v", each.written, errorValue)
		}
		if got := first.Format("2006-01-02T15:04"); got != each.first {
			t.Errorf("%s starts %s, want %s", each.written, got, each.first)
		}
		if got := last.Format("2006-01-02T15:04"); got != each.last {
			t.Errorf("%s ends %s, want %s", each.written, got, each.last)
		}
		if first.Location() != seoul {
			t.Errorf("%s reads in %s, want the store's location", each.written, first.Location())
		}
	}
}

func TestAnOccurrenceCarriesItsOwnOffsetWhenTheTextGaveOne(t *testing.T) {
	occurrence, errorValue := bluememo.ParseOccurrence("2026-03-05T06:30Z")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	seoul, _ := time.LoadLocation("Asia/Seoul")
	first, _, errorValue := occurrence.Span(seoul)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if got := first.In(seoul).Format("2006-01-02T15:04"); got != "2026-03-05T15:30" {
		t.Errorf("an instant reads %s in Seoul, want 2026-03-05T15:30", got)
	}
}

func TestAnOccurrenceInNoKnownFormIsRefused(t *testing.T) {
	for _, written := range []string{"2026-13", "2026-Q5", "2026-H3", "last year", "2026/", "26-03-05", "2026-03-05T15"} {
		if _, errorValue := bluememo.ParseOccurrence(written); errorValue == nil {
			t.Errorf("%q was accepted", written)
		}
	}
	for _, written := range []string{"", "  "} {
		occurrence, errorValue := bluememo.ParseOccurrence(written)
		if errorValue != nil || !occurrence.IsZero() {
			t.Errorf("%q should read as no occurrence, got %q %v", written, occurrence, errorValue)
		}
	}
}
