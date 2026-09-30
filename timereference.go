package bluememo

import (
	"fmt"
	"time"
)

func (store *Store) embeddingText(content string, occurredAt time.Time) string {
	if !store.configuration.EmbedTimeReference || occurredAt.IsZero() {
		return content
	}
	return timeReference(occurredAt, store.now(), store.configuration.Location) + ": " + content
}

func timeReference(occurredAt time.Time, now time.Time, location *time.Location) string {
	occurred := occurredAt.In(location)
	return fmt.Sprintf("%d년 %d월 %d일, %s", occurred.Year(), int(occurred.Month()), occurred.Day(),
		relativeMonth(monthsBetween(occurred, now.In(location))))
}

func monthsBetween(from time.Time, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
}

func relativeMonth(monthsAgo int) string {
	switch {
	case monthsAgo == 0:
		return "이번 달"
	case monthsAgo == 1:
		return "지난달"
	case monthsAgo == -1:
		return "다음 달"
	case monthsAgo > 0:
		return fmt.Sprintf("%d개월 전", monthsAgo)
	}
	return fmt.Sprintf("%d개월 후", -monthsAgo)
}
