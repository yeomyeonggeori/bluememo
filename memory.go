package bluememo

import (
	"errors"
	"strings"
	"time"
)

const ContentCharacterLimit = 240

type Expiry string

const (
	ExpiryNone         Expiry = "none"
	ExpiryEndOfToday   Expiry = "end_of_today"
	ExpiryEndOfWeek    Expiry = "end_of_week"
	ExpiryEndOfMonth   Expiry = "end_of_month"
	ExpiryEndOfQuarter Expiry = "end_of_quarter"
	ExpiryEndOfYear    Expiry = "end_of_year"
	ExpiryOnDate       Expiry = "on_date"
)

var Expiries = []Expiry{ExpiryNone, ExpiryEndOfToday, ExpiryEndOfWeek, ExpiryEndOfMonth, ExpiryEndOfQuarter, ExpiryEndOfYear, ExpiryOnDate}

type ColdReason string

const (
	ColdReasonPressure   ColdReason = "pressure"
	ColdReasonSuperseded ColdReason = "superseded"
	ColdReasonExpired    ColdReason = "expired"
)

var ColdReasons = []ColdReason{ColdReasonPressure, ColdReasonSuperseded, ColdReasonExpired}

type TombstoneReason string

const TombstoneReasonAsked TombstoneReason = "asked"

var TombstoneReasons = []TombstoneReason{TombstoneReasonAsked, TombstoneReason(ColdReasonPressure), TombstoneReason(ColdReasonSuperseded), TombstoneReason(ColdReasonExpired)}

type Edge string

const (
	EdgeUpdates Edge = "updates"
	EdgeExtends Edge = "extends"
)

var Edges = []Edge{EdgeUpdates, EdgeExtends}

type Proposition struct {
	Content    string `json:"content"`
	IsStatic   bool   `json:"isStatic"`
	OccurredOn string `json:"occurredOn"`
	Expiry     Expiry `json:"expiry"`
	ExpiryDate string `json:"expiryDate"`
}

type Memory struct {
	MemoryID          string     `json:"memoryID"`
	Content           string     `json:"content"`
	IsStatic          bool       `json:"isStatic"`
	OccurredAt        time.Time  `json:"occurredAt,omitzero"`
	ValidUntil        time.Time  `json:"validUntil,omitzero"`
	OriginID          string     `json:"originID"`
	Importance        int        `json:"importance"`
	StorageStrength   float64    `json:"storageStrength"`
	ResolvedEntityIDs []string   `json:"resolvedEntityIDs"`
	UnresolvedNames   []string   `json:"unresolvedNames"`
	CreatedAt         time.Time  `json:"createdAt"`
	LastRecalledAt    time.Time  `json:"lastRecalledAt,omitzero"`
	ColdSince         time.Time  `json:"coldSince,omitzero"`
	ColdReason        ColdReason `json:"coldReason,omitempty"`
	SupersededBy      string     `json:"supersededBy,omitempty"`
}

func (memory Memory) IsCold() bool {
	return !memory.ColdSince.IsZero()
}

type Tombstone struct {
	MemoryID      string          `json:"memoryID"`
	Content       string          `json:"content"`
	IsStatic      bool            `json:"isStatic"`
	OccurredAt    time.Time       `json:"occurredAt,omitzero"`
	OriginID      string          `json:"originID"`
	Reason        TombstoneReason `json:"reason"`
	RequestPhrase string          `json:"requestPhrase,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	DiedAt        time.Time       `json:"diedAt"`
}

var (
	ErrEmptyContent      = errors.New("a memory needs content")
	ErrContentTooLong    = errors.New("a memory is one sentence of at most 240 characters")
	ErrUnknownExpiry     = errors.New("expiry is not one of the declared values")
	ErrExpiryDateMissing = errors.New("expiry on_date needs an expiryDate of the form YYYY-MM-DD")
	ErrAlreadyExpired    = errors.New("the proposition stopped being true before it arrived")
)

type dated struct {
	occurredAt time.Time
	validUntil time.Time
}

func resolveDates(proposition Proposition, arrivedAt time.Time, location *time.Location) (dated, error) {
	content := strings.TrimSpace(proposition.Content)
	if content == "" {
		return dated{}, ErrEmptyContent
	}
	if len([]rune(content)) > ContentCharacterLimit {
		return dated{}, ErrContentTooLong
	}
	validUntil, errorValue := ExpiryInstant(proposition.Expiry, proposition.ExpiryDate, arrivedAt, location)
	if errorValue != nil {
		return dated{}, errorValue
	}
	if !validUntil.IsZero() && !validUntil.After(arrivedAt) {
		return dated{}, ErrAlreadyExpired
	}
	return dated{occurredAt: parseDay(proposition.OccurredOn, location), validUntil: validUntil}, nil
}

func ExpiryInstant(expiry Expiry, expiryDate string, arrivedAt time.Time, location *time.Location) (time.Time, error) {
	local := arrivedAt.In(location)
	startOfToday := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	switch expiry {
	case ExpiryNone, "":
		return time.Time{}, nil
	case ExpiryEndOfToday:
		return startOfToday.AddDate(0, 0, 1), nil
	case ExpiryEndOfWeek:
		daysSinceMonday := (int(local.Weekday()) + 6) % 7
		return startOfToday.AddDate(0, 0, 7-daysSinceMonday), nil
	case ExpiryEndOfMonth:
		return time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, location), nil
	case ExpiryEndOfQuarter:
		firstMonthOfNextQuarter := time.Month((int(local.Month())-1)/3*3 + 4)
		return time.Date(local.Year(), firstMonthOfNextQuarter, 1, 0, 0, 0, 0, location), nil
	case ExpiryEndOfYear:
		return time.Date(local.Year()+1, time.January, 1, 0, 0, 0, 0, location), nil
	case ExpiryOnDate:
		lastDay := parseDay(expiryDate, location)
		if lastDay.IsZero() {
			return time.Time{}, ErrExpiryDateMissing
		}
		return lastDay.AddDate(0, 0, 1), nil
	}
	return time.Time{}, ErrUnknownExpiry
}

func parseDay(value string, location *time.Location) time.Time {
	day, errorValue := time.ParseInLocation(time.DateOnly, strings.TrimSpace(value), location)
	if errorValue != nil {
		return time.Time{}
	}
	return day
}
