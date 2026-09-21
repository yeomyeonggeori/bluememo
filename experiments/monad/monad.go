package main

import (
	"errors"
	"strings"
	"time"
)

// Monad is everything the model produces. Three fields, nothing else.
type Monad struct {
	Content    string `json:"content"`
	IsStatic   bool   `json:"isStatic"`
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

func validateMonad(monad Monad) error {
	if strings.TrimSpace(monad.Content) == "" {
		return errors.New("monad content is empty")
	}
	return nil
}
