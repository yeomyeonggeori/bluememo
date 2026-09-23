# bluememo

*Long-term memory for one agent serving one person: a single SQLite file of self-contained sentences that settles what it hears, recalls without a model call, and forgets under pressure.*

[![check](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml/badge.svg)](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

> **Status: pre-alpha.** The exported API, the schema and the prompts change without notice.

The design and the measurements behind it are in [issue #3](https://github.com/yeomyeonggeori/bluememo/issues/3).

## One file, one person

`Open` takes a path and creates it with mode `0600`. There is no default location: the host decides whose file it is, and an empty path fails with `ErrNoPath`. The store has no access-control model: whoever can open the file can read all of it, so a host that serves several people gives each one their own file and opens it as that person. Nothing is shared between files.

It depends on the standard library and `modernc.org/sqlite`, which is pure Go. Search is exact: every live vector is compared to the query. On an Apple-silicon laptop a recall over 10,000 memories with 1,024-dimension vectors takes about 85 ms. Settling compares each new sentence with every live memory, about 30 ms at that size, which the judge's model calls outweigh.

## A memory

A memory is one sentence that stands on its own, with pronouns resolved and conditions kept (`이샘플은 회의록을 항상 마크다운으로 받고 싶어 한다.`). The model supplies four things:

| field | meaning |
|---|---|
| `content` | the sentence |
| `isStatic` | a permanent trait; static memories are the profile |
| `occurredOn` | the day it happened, if it is an event |
| `expiry` | when it stops being true: `none`, `end_of_today`, `end_of_week`, `end_of_month`, `end_of_quarter`, `end_of_year`, or `on_date` with `expiryDate` |

The runtime computes the actual expiry instant from the note's arrival time and the configured `Location`. Date arithmetic stays out of the model. The runtime also records `storage_strength`, `origin_id` (the batch the sentence came from), entity IDs from an optional `EntityResolver`, and timestamps.

## Writing: memorize, then settle

`Memorize` stores a raw note and returns. `Settle`, run in the background as the same person, claims each group of notes with a lease, asks the `LanguageModel` to decompose it into sentences, and asks the `Judge` how each sentence stands to its five nearest live memories:

| relation | effect |
|---|---|
| `unrelated` | inserted |
| `extends` | inserted, linked to the memory it adds detail to |
| `updates` | inserted; the old memory goes cold as superseded |
| `same` | the higher-rated wording stays live and is reinforced; the other goes cold |
| `noise` | dropped |

Embeddings only gather candidates. The judge decides. `DistributionJudge` reads a probability distribution over single-digit answers from a host-provided `Chooser` (typed logits) and falls back to `unrelated` when the leading answer is not ahead by a margin: 0.10 for most relations and 0.25 for `same`, the one branch that takes a sentence out of retrieval.

Sentences the judge rates 3 or higher are rehearsed: the model writes up to four trigger phrases, and a phrase is kept only when it sits at least 0.05 closer to its own memory than to the average live memory. Until a note settles, `Recall` returns it under `Unsettled` when it shares words with the query.

## Reading

`Recall` makes no model call. It fuses three lanes by reciprocal rank: cosine over memory vectors, character-bigram overlap over the text (two-syllable Korean words count), and cosine over trigger phrases. The leading hit brings the rest of its origin bundle along. Age plays no part in ranking; a question about last year wants last year's memory. Superseded and expired memories never answer. `Profile` returns the live static memories.

Every recalled memory is reinforced by `1 − retrievability`, so a memory recalled just before it would have faded gains the most. A memory that pressure pushed cold still answers, comes back live, and gains the maximum.

## Forgetting

`Forget` takes a phrase and resolves it: an exact memory ID, then an exact sentence, then a phrase exactly one live memory contains. One match is deleted. Several matches, or none, come back as candidates and nothing is deleted; the caller confirms with `ForgetMemories`. Every deletion leaves a tombstone with the sentence, its origin, why it died, and the phrase that asked.

`Sweep` only deletes under pressure:

1. memories past their expiry go cold;
2. static memories beyond `StaticShare` (20%) of `Capacity` lose the static flag, weakest first;
3. while live memories exceed `Capacity` (10,000), the least useful origin bundles go cold together;
4. while stored memories exceed `Capacity`, cold memories past `ColdGrace` (30 days) are deleted, superseded first, then expired, then pressure;
5. tombstones beyond `TombstoneCapacity` are pruned, oldest first.

Usefulness is `storage_strength × retrievability`, and retrievability halves every `HalfLife × storage_strength` (30 days for a memory stored once) since it was last recalled. These defaults are starting points that nothing has measured yet.

## Using it

```go
store, _ := bluememo.Open(ctx, "/workspace/private/people/<personID>/memory.db", bluememo.Configuration{
	Embedder:       yourEmbedder,      // EmbedQuery / EmbedDocuments
	EmbeddingModel: "your-model-name", // vectors from another model do not rank until Reembed
	Model:          yourStructuredModel,
	Judge:          bluememo.DistributionJudge{Chooser: yourLogprobChooser},
	Location:       seoul,
})
_ = store.Memorize(ctx, bluememo.Note{GroupID: conversationID, Body: text, SpeakerName: name, IsExplicit: true})
_, _ = store.Settle(ctx)
result, _ := store.Recall(ctx, question, 12)
_, _ = store.Sweep(ctx)
```

`go run ./examples/recall` runs the whole path with the scripted model and hash embedder from `bluememotest`.

## Layout

| path | holds |
|---|---|
| `.` | the store, settling, judging, recall, forgetting, sweeping |
| `migrations/` | the schema, embedded and applied by `Open` through `user_version` |
| `bluememotest/` | a hash embedder, a table embedder, a scripted model, judge and chooser |
