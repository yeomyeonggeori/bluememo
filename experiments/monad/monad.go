package main

import (
	"errors"
	"strings"
	"time"
)

type Kind string

const (
	KindIdentity   Kind = "identity"
	KindPreference Kind = "preference"
	KindFact       Kind = "fact"
	KindEpisode    Kind = "episode"
	KindProcedure  Kind = "procedure"
	KindUnknown    Kind = "unknown"
)

var writableKinds = []Kind{KindIdentity, KindPreference, KindFact, KindEpisode, KindProcedure}

// Monad is everything the model produces. Three fields, nothing else.
type Monad struct {
	Content    string `json:"content"`
	Kind       Kind   `json:"kind"`
	ValidUntil string `json:"validUntil"`
}

// Memory is a Monad plus what the runtime knows. The model never supplies these.
type Memory struct {
	Monad
	MemoryID          string
	ResolvedEntityIDs []string
	UnresolvedNames   []string
	StorageStrength   float64
	RetrievalStrength float64
	CreatedAt         time.Time
	LastRecalledAt    time.Time
}

func (monad Monad) settledKind() Kind {
	if monad.Kind == KindUnknown || monad.Kind == "" {
		return KindFact
	}
	return monad.Kind
}

func validateMonad(monad Monad) error {
	if strings.TrimSpace(monad.Content) == "" {
		return errors.New("monad content is empty")
	}
	for _, kind := range append(writableKinds, KindUnknown) {
		if monad.Kind == kind {
			return nil
		}
	}
	return errors.New("monad kind is not one of the declared values: " + string(monad.Kind))
}
