package main

import "context"

// Decomposer turns natural language into self-contained propositions.
// It does not know which operation it serves.
type Decomposer interface {
	Decompose(ctx context.Context, text string) ([]Monad, error)
}

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EntityResolver fills resolved identifiers only when a name matches exactly one
// known person. Ambiguity resolves nothing and is reported instead.
type EntityResolver interface {
	Resolve(content string) (resolved []string, unresolved []string)
}
