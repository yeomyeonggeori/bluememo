# bluememo

*Long-term memory for an agent that works for a company: one Postgres store, facts a model extracts, scopes a reader's clearance decides.*

[![check](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml/badge.svg)](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

> **Status: pre-alpha.** The exported API, the schema and the prompts change without notice.

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

`Store.Recall` returns a person's **profile** (two short lists a background job condenses from their facts) and a hybrid search of the prompt: pgvector cosine and `pg_trgm` word similarity fused by reciprocal rank, reinforcement breaking ties. Age does not lower a fact's rank. A measurement of the alternative put age-weighted ranking below plain similarity, because a question about the past is exactly where an old fact is wanted; age decides which facts an eviction sweep takes, not which ones a search returns. Two things widen what a search can reach. A hit pulls in the other facts extracted from the same episode, so a question answered by one sentence returns the sentences that came with it. And each fact carries **trigger phrases**: short situations a model writes at rehearsal time, embedded and searched beside the facts themselves, returning the fact rather than the phrase. A phrase survives only when it sits measurably closer to its own fact than to that fact's neighbours, so one that fits anything is dropped instead of drawing every query toward one memory. `Store.EnqueueRehearse` and `RehearseJobHandler` run this after ingest; facts written before it exist have no phrases and answer as they always did.

Where the `vector` extension is absent the migration still applies and search answers lexically; the result says which mode answered. Vectors from two embedding models do not compare, so the vector side of a search only ranks facts embedded by the store's own model; a `reembed` job (`Store.EnqueueReembed`, `ReembedJobHandler`) moves every live fact onto that model, and until it has run those facts answer lexically.

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

recall, _ := store.Recall(ctx, bluememo.RecallRequest{Reader: reader, Query: prompt})
result, _ := ingester.Ingest(ctx, bluememo.IngestRequest{Episode: episode, Reader: reader, Label: label})
```

Run the reader-scoped in-memory example with `go run ./examples/recall`. It seeds synthetic facts in `InMemoryRepository` and needs no database or model. Production ingestion uses `Ingester` with a host-provided model and embedder.

`JobWorker` drains `memory_job` (extraction, profile rebuilds, trigger rehearsal) with `FOR UPDATE SKIP LOCKED` claims, leases and backoff; `InMemoryRepository` and `bluememotest` carry the same contract for tests.

The host authenticates every `Reader` and resolves its current circle membership and clearance. Repository methods are privileged storage operations; expose the `Store` methods to authenticated callers. Recall always targets the reader's own profile. `ProfileJobHandler.ResolveReader` must resolve that person's current access from the host directory each time a job runs.

Profiles record the IDs of their source facts. Before returning a profile, the store compares those IDs with the reader's currently readable live facts. A change in membership, clearance, fact validity or content invalidates the cached result and queues a rebuild. Existing profiles without source IDs stay stored and are withheld until rebuilt.

An episode's `(source_kind, source_id)` is its idempotency key. Replaying the same requester and payload returns the committed receipt without repeating extraction or reinforcement. A different requester, conversation or content under the same key fails. Receipts contain IDs; replay reads fact content through current permissions and validity checks. Receipts reconstructed for pre-upgrade episodes cannot recover historical reinforcement IDs or candidate counts that were never recorded.

Fact changes and profile-job enqueueing commit together. Each job claim carries a token and generation: a worker whose lease was reclaimed cannot settle another worker's claim, and work enqueued during execution stays pending. `ApplyMigrations` serializes callers and commits pending migrations with their ledger entries in one transaction. Back up the database before upgrading; failed migrations leave their schema changes unapplied.

## Layout

| path | holds |
|---|---|
| `.` | types, validation, reader, ranking, store, ingest, profile, worker, in-memory repository |
| `postgres/` | the repositories and `ApplyMigrations` |
| `migrations/` | the schema, embedded |
| `bluememotest/` | a deterministic embedder and a scripted model |

Tests that need Postgres read `BLUEMEMO_TEST_POSTGRES_URL` and skip without it.

The current embedding contract is fixed at 1,024 dimensions, with one database per tenant. This is pre-alpha software; pin a release, since breaking API changes are allowed between pre-release versions. Schema upgrades use additive migrations, and `ApplyMigrations` does not perform an automatic downgrade. Forgetting a fact hides it from recall while retaining its row and audit metadata; backups and any eventual erasure policy remain the host's responsibility.
