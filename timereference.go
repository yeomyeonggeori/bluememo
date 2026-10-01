package bluememo

import "time"

func (store *Store) matchableText(content string, occurredAt time.Time, occurredUntil time.Time) string {
	if !store.configuration.EmbedTimeReference || occurredAt.IsZero() {
		return content
	}
	return timeReference(occurredAt, occurredUntil, store.configuration.Location) + ": " + content
}

func timeReference(occurredAt time.Time, occurredUntil time.Time, location *time.Location) string {
	first := formatMoment(occurredAt.In(location))
	if occurredUntil.IsZero() {
		return first
	}
	return first + "/" + formatMoment(occurredUntil.In(location))
}

func formatMoment(moment time.Time) string {
	if isStartOfDay(moment) {
		return moment.Format(time.DateOnly)
	}
	return moment.Format(momentLayout)
}

func isStartOfDay(moment time.Time) bool {
	return moment.Hour() == 0 && moment.Minute() == 0 && moment.Second() == 0 && moment.Nanosecond() == 0
}

const (
	momentLayout           = "2006-01-02T15:04"
	momentWithOffsetLayout = "2006-01-02T15:04Z07:00"
)
