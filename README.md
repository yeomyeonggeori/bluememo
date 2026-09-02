# bluememo

*Long-term memory for an agent that works for a company: one Postgres store, facts a model extracts, scopes a reader's clearance decides.*

[![check](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml/badge.svg)](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

> **Status: pre-alpha.** The exported API, the schema and the prompts change without notice. Pin a commit.

## The model

An **episode** is something that happened: a finished task, or a sentence a person asked the agent to keep. A **fact** is one atomic sentence a language model extracts from an episode, with a kind (`identity`, `preference`, `fact`, `episode`, `temporary`), a scope, and the security label of the conversation it came from. Facts are never deleted. A newer fact supersedes an older one, a repeated fact reinforces one, a person forgets one with a reason, and a temporary fact expires.

Every fact has an owner, the person whose task or request produced it, and zero or more circles it is shared with:

| circles | who reads it |
|---|---|
| none | its owner, and nobody else |
| one or more | its owner, plus members of any named circle whose security rank and classes pass the label |

There is no company-wide scope: sharing with everyone is sharing with the circle everyone is in. Circles nest. A circle that is a member of another circle is readable from that other circle, transitively, so a fact shared with `platform` is readable by everyone in `engineering` when `engineering` contains `platform`. The host supplies the containment map; bluememo computes the readable set once per reader and applies it as one SQL predicate. Writing to a circle needs direct membership.

## Writing

Everything goes through `Ingester`. It embeds the episode, offers the reader's nearest live facts to the model as candidates, and the model returns the facts the memory should hold afterwards, each related to a candidate as `new`, `supersedes`, or `reinforces`. The runtime rejects a relation to any fact it did not offer, narrows a circle fact to the circles the requester is in, and strips the label from private facts. No similarity threshold merges facts on its own.

## Reading

`Store.Recall` returns a person's **profile** (two short lists a background job condenses from their facts) and a hybrid search of the prompt: pgvector cosine and `pg_trgm` word similarity fused by reciprocal rank, episodes decaying with age, reinforcement breaking ties. Where the `vector` extension is absent the migration still applies and search answers lexically; the result says which mode answered.

## Using it

```go
database, _ := sql.Open("pgx", url)
_ = postgres.ApplyMigrations(ctx, database)

store := bluememo.Store{
	Facts:          postgres.NewFactRepository(database),
	Profiles:       postgres.NewProfileRepository(database),
	Jobs:           postgres.NewJobRepository(database),
	Embedder:       yourEmbedder,   // EmbedQuery / EmbedDocuments, 1,024 dimensions
	EmbeddingModel: bluememo.DefaultEmbeddingModelName,
}
ingester := bluememo.Ingester{Store: store, Model: yourStructuredModel}
reader := bluememo.NewReader(personID, memberCircleIDs, containedCircles, rank, classes)

recall, _ := store.Recall(ctx, bluememo.RecallRequest{Reader: reader, PersonID: personID, Query: prompt})
result, _ := ingester.Ingest(ctx, bluememo.IngestRequest{Episode: episode, Reader: reader, Label: label})
```

`JobWorker` drains `memory_job` (extraction, profile rebuilds) with `FOR UPDATE SKIP LOCKED` claims, leases and backoff; `InMemoryRepository` and `bluememotest` carry the same contract for tests.

## Layout

| path | holds |
|---|---|
| `.` | types, validation, reader, ranking, store, ingest, profile, worker, in-memory repository |
| `postgres/` | the repositories and `ApplyMigrations` |
| `migrations/` | the schema, embedded |
| `bluememotest/` | a deterministic embedder and a scripted model |

Tests that need Postgres read `BLUEMEMO_TEST_POSTGRES_URL` and skip without it.
