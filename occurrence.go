package bluememo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Occurrence is when something happened, written at the precision the text gave
// it and no narrower. A year that was all the text offered stays a year, so a
// reader is never handed a day nobody said.
//
//	2026              a year
//	2026-H1           a half
//	2026-Q3           a quarter
//	2026-03           a month
//	2026-03-05        a day
//	2026-03-05T15:30  a time of day, in the store's location
//	2026-03-05T06:30Z an instant, carrying its own offset
//	2026-03/2026-05   a stretch, written as its first and last part
//
// It is ordered and comparable as text within one precision and never silently
// widened. Nothing stores the endpoints a precision implies, so the two can
// never disagree; Span derives them when a caller needs instants.
type Occurrence string

const (
	yearLayout     = "2006"
	monthLayout    = "2006-01"
	dayLayout      = time.DateOnly
	minuteLayout   = "2006-01-02T15:04"
	instantLayout  = "2006-01-02T15:04Z07:00"
	halfPattern    = `^(\d{4})-H([12])$`
	quarterPattern = `^(\d{4})-Q([1-4])$`
)

var (
	halfExpression    = regexp.MustCompile(halfPattern)
	quarterExpression = regexp.MustCompile(quarterPattern)
)

// ErrOccurrence is a written occurrence in none of the forms above.
var ErrOccurrence = fmt.Errorf("an occurrence is a year, a half, a quarter, a month, a day, a time, or two of those joined by a slash")

func ParseOccurrence(written string) (Occurrence, error) {
	trimmed := strings.TrimSpace(written)
	if trimmed == "" {
		return "", nil
	}
	for _, part := range strings.SplitN(trimmed, "/", 2) {
		if _, _, errorValue := partSpan(part, time.UTC); errorValue != nil {
			return "", fmt.Errorf("%q: %w", written, ErrOccurrence)
		}
	}
	return Occurrence(trimmed), nil
}

func (occurrence Occurrence) IsZero() bool { return occurrence == "" }

// Span is the first and last instant the occurrence covers, which a caller
// needs for ordering or for a window and nothing needs for storage.
func (occurrence Occurrence) Span(location *time.Location) (time.Time, time.Time, error) {
	if occurrence.IsZero() {
		return time.Time{}, time.Time{}, nil
	}
	parts := strings.SplitN(string(occurrence), "/", 2)
	first, firstEnd, errorValue := partSpan(parts[0], location)
	if errorValue != nil {
		return time.Time{}, time.Time{}, errorValue
	}
	if len(parts) == 1 {
		return first, firstEnd, nil
	}
	_, lastEnd, errorValue := partSpan(parts[1], location)
	if errorValue != nil {
		return time.Time{}, time.Time{}, errorValue
	}
	return first, lastEnd, nil
}

func partSpan(part string, location *time.Location) (time.Time, time.Time, error) {
	if match := halfExpression.FindStringSubmatch(part); match != nil {
		year, half := numberIn(match[1]), numberIn(match[2])
		start := time.Date(year, time.Month(1+(half-1)*6), 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 6, 0).Add(-time.Nanosecond), nil
	}
	if match := quarterExpression.FindStringSubmatch(part); match != nil {
		year, quarter := numberIn(match[1]), numberIn(match[2])
		start := time.Date(year, time.Month(1+(quarter-1)*3), 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 3, 0).Add(-time.Nanosecond), nil
	}
	for _, shape := range []struct {
		layout string
		add    func(time.Time) time.Time
	}{
		{instantLayout, func(moment time.Time) time.Time { return moment.Add(time.Minute) }},
		{minuteLayout, func(moment time.Time) time.Time { return moment.Add(time.Minute) }},
		{dayLayout, func(moment time.Time) time.Time { return moment.AddDate(0, 0, 1) }},
		{monthLayout, func(moment time.Time) time.Time { return moment.AddDate(0, 1, 0) }},
		{yearLayout, func(moment time.Time) time.Time { return moment.AddDate(1, 0, 0) }},
	} {
		parsed, errorValue := time.ParseInLocation(shape.layout, part, location)
		if errorValue == nil {
			return parsed, shape.add(parsed).Add(-time.Nanosecond), nil
		}
	}
	return time.Time{}, time.Time{}, fmt.Errorf("%q: %w", part, ErrOccurrence)
}

func numberIn(digits string) int {
	value, _ := strconv.Atoi(digits)
	return value
}
