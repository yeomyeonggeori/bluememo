package bluememo

import (
	"fmt"
	"time"
)

func (store *Store) matchableText(content string, occurredAt time.Time, occurredUntil time.Time) string {
	if !store.configuration.EmbedTimeReference || occurredAt.IsZero() {
		return content
	}
	return timeReference(occurredAt, occurredUntil, store.configuration.Location) + ": " + content
}

func timeReference(occurredAt time.Time, occurredUntil time.Time, location *time.Location) string {
	first := occurredAt.In(location)
	if occurredUntil.IsZero() {
		return formatDay(first)
	}
	last := occurredUntil.In(location)
	switch {
	case coversWholeYear(first, last):
		return fmt.Sprintf("%d년", first.Year())
	case coversWholeQuarter(first, last):
		return fmt.Sprintf("%d년 %d분기", first.Year(), (int(first.Month())-1)/3+1)
	case coversWholeMonth(first, last):
		return fmt.Sprintf("%d년 %d월", first.Year(), int(first.Month()))
	}
	return formatDay(first) + "부터 " + formatDay(last) + "까지"
}

func formatDay(day time.Time) string {
	return fmt.Sprintf("%d년 %d월 %d일", day.Year(), int(day.Month()), day.Day())
}

func coversWholeMonth(first time.Time, last time.Time) bool {
	return coversMonths(first, last, 1)
}

func coversWholeQuarter(first time.Time, last time.Time) bool {
	return (int(first.Month())-1)%3 == 0 && coversMonths(first, last, 3)
}

func coversWholeYear(first time.Time, last time.Time) bool {
	return first.Month() == time.January && coversMonths(first, last, 12)
}

func coversMonths(first time.Time, last time.Time, monthCount int) bool {
	if first.Day() != 1 {
		return false
	}
	dayAfterLast := time.Date(first.Year(), first.Month()+time.Month(monthCount), 1, 0, 0, 0, 0, first.Location())
	return sameDay(last.AddDate(0, 0, 1), dayAfterLast)
}

func sameDay(left time.Time, right time.Time) bool {
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}
