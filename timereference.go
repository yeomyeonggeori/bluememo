package bluememo

import (
	"fmt"
	"time"
)

func (store *Store) embeddingText(content string, occurredAt time.Time) string {
	if !store.configuration.EmbedTimeReference || occurredAt.IsZero() {
		return content
	}
	return timeReference(occurredAt, store.configuration.Location) + ": " + content
}

func timeReference(occurredAt time.Time, location *time.Location) string {
	occurred := occurredAt.In(location)
	return fmt.Sprintf("%d년 %d월 %d일", occurred.Year(), int(occurred.Month()), occurred.Day())
}
