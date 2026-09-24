# Overview

bluememo is long-term memory for an agent that works for one person. It keeps what the agent learned in a single SQLite file of self-contained sentences, settles what it hears in the background, answers a question without calling a model, and forgets only when it runs out of room.

## Why it exists

An agent that forgets everything between conversations makes a person repeat themselves. An agent that remembers everything forever drowns its own answers in stale notes. bluememo sits between the two. It keeps the sentences that change how the agent should act next time, lets a newer sentence replace an older one, and clears out what nobody has needed once the store is full.

## What it is

- **One file per person.** `Open` takes a path and creates the file with mode `0600`. There is no access-control model inside: whoever can open the file can read all of it, so a host that serves several people opens a different file for each one, as that person.
- **Sentences, not triples.** A memory is one sentence that stands on its own: `Alex wants meeting notes in Markdown every time.` Conditions, time and source stay inside the sentence.
- **Pure Go.** It depends on the standard library and `modernc.org/sqlite`. The host supplies the embedder, the language model and the judge through small interfaces.

## What it is not

It is not a document store: files are turned into text by the host before they arrive. It is not shared memory either. Knowledge a whole company should agree on belongs in the company's documents and database.

## Where to go next

- [Quickstart](#quickstart) runs the whole path in a few lines.
- [Concepts](#concepts) defines the words the rest of the pages use.
- [Lifecycle](#lifecycle) follows a note from arrival to tombstone.
- [Ports](#ports) lists what a host has to supply.

# Quickstart

This runs on a laptop with [Ollama](https://ollama.com) and two small models:

```bash
ollama pull qwen3.5:4b
ollama pull embeddinggemma
go get github.com/yeomyeonggeori/bluememo
```

Alex tells the agent three things once. Later the agent has to write to Alex and asks the store how:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/ollama"
)

func main() {
	ctx := context.Background()
	model := ollama.New("qwen3.5:4b", "embeddinggemma")

	store, errorValue := bluememo.Open(ctx, "alex.db", bluememo.Configuration{
		Embedder:       model,
		EmbeddingModel: model.EmbeddingModel,
		Model:          model,
		Judge:          bluememo.DistributionJudge{Chooser: model},
	})
	if errorValue != nil {
		log.Fatal(errorValue)
	}
	defer store.Close()

	store.Memorize(ctx, bluememo.Note{
		SpeakerName: "Alex",
		Body:        "I lead the payments team. Send me meeting notes in Markdown, and I'm off every Friday this month.",
	})
	if _, errorValue := store.Settle(ctx); errorValue != nil {
		log.Fatal(errorValue)
	}

	result, errorValue := store.Recall(ctx, "How should I send Alex the notes from today's meeting?", 3)
	if errorValue != nil {
		log.Fatal(errorValue)
	}
	for _, recalled := range result.Memories {
		fmt.Println(recalled.Memory.Content)
	}
}
```

```text
Alex wants meeting notes in Markdown.
Alex leads the payments team.
Alex is off every Friday this month.
```

The one message became three sentences that each stand on their own, and the one about notes answers first. `Alex leads the payments team.` is stored as a permanent trait, so `store.Profile(ctx)` returns it for every conversation, and the Friday sentence expires on the first of next month without anyone deleting it.

The same program is in [`examples/quickstart`](https://github.com/yeomyeonggeori/bluememo/tree/main/examples/quickstart). `ollama` is a small adapter for local models; a host with its own model client implements the three [ports](#ports) instead.

# Concepts

## Memory

One self-contained sentence the store keeps, with the dates and strength the runtime attached to it.

The model supplies four things when it writes one:

| field | meaning |
| --- | --- |
| `content` | the sentence, at most 240 characters, with pronouns resolved |
| `isStatic` | a permanent trait of the person, such as a name, a role or a lasting preference |
| `occurredOn` | the day it happened, when it describes an event |
| `expiry` | when it stops being true, chosen from a closed list |

The runtime adds the rest: an identifier, the origin it came from, its importance rating, its storage strength, the people it names, and timestamps. A memory is live, cold, or gone.

## Note

The raw text a host hands to `Memorize`, kept until settling turns it into memories.

A note carries a group identifier, the speaker's name and whether the person asked for it to be remembered. Notes that share a group are decomposed together, so the model reads a conversation as one unit. Until a note settles, `Recall` returns it under `Unsettled` when it shares words with the question.

## Origin

The group of memories that came out of one settled group of notes.

Decomposition splits one message into several sentences, and those siblings belong together: `A release ships admind and capabilityd together` and `Shipping only one of them breaks the protocol` answer the same question. A recall that finds one of them returns the others with it, and eviction moves or keeps a whole origin at once.

## Expiry

The point at which a memory stops being true, chosen by the model and computed by the runtime.

The model picks one of `none`, `end_of_today`, `end_of_week`, `end_of_month`, `end_of_quarter`, `end_of_year`, or `on_date` with an `expiryDate`. The runtime turns that choice into an instant from the note's arrival time in the configured `Location`, so "only this quarter" said on 21 September becomes 1 October 00:00. Date arithmetic stays out of the model because small models get it wrong far more often than they get the period wrong.

## Strength

How much a memory has been needed, which decides what survives when the store is full.

`storage_strength` starts at 1, or 2 when the person asked for the memory to be kept, and only ever grows. Retrievability is computed from it rather than stored: it halves every `HalfLife × storage_strength` since the memory was last recalled. Usefulness is their product, and it orders eviction. It never orders search results.

## Cold

A memory that has left the live set but still exists.

A memory goes cold for one of three reasons. `pressure`: the store was over capacity and its origin was the least useful. `superseded`: a newer memory replaced it. `expired`: its expiry passed. Superseded and expired memories never answer a recall. A memory that pressure pushed out still answers, comes back live, and gains the maximum reinforcement.

## Tombstone

What remains after a memory is deleted.

A tombstone keeps the sentence, whether it was static, when it happened, its origin, why it died (`asked`, `pressure`, `superseded` or `expired`) and, when a person asked, the phrase they used. A deleted memory can be read back from its tombstone alone. Tombstones have their own cap.

# Lifecycle

## Memorize

Queues a note and returns without deciding anything.

```go
func (store *Store) Memorize(ctx context.Context, note Note) error
```

An empty body fails with `ErrEmptyNote`. A note without a group identifier gets a group of its own. What goes in is the host's call; a host that keeps confidential documents out of memory simply never memorizes a document tool's output.

## Settle

Turns pending notes into memories, one group at a time.

```go
func (store *Store) Settle(ctx context.Context) (SettleReport, error)
```

For each group, settling claims the group with a lease so two workers never process it at once, asks the language model to decompose it into sentences, and then, for each sentence, shows the judge the five nearest live memories and applies its verdict:

| relation | effect |
| --- | --- |
| `unrelated` | inserted |
| `extends` | inserted, and linked to the memory it adds detail to |
| `updates` | inserted, and the old memory goes cold as superseded |
| `same` | the higher-rated wording stays live and is reinforced; the other goes cold |
| `noise` | dropped: the judge rated the sentence 1, nothing worth keeping |

Embeddings only gather the candidates; the judge decides. When the store holds nothing close yet, there is nothing to relate to, and the sentence is kept unless it is noise. A sentence the judge rates 3 or higher is rehearsed: the model writes up to four trigger phrases a person might use when the memory matters, and a phrase is kept only if it sits at least 0.05 closer to its own memory than to the average live memory. A group whose settling fails keeps its lease until it runs out, then the next `Settle` takes it again.

The report counts groups, proposals, inserts, reinforcements, supersessions, extensions, drops, rejected sentences and failed rehearsals.

## Recall

Answers a question from the store without a model call.

```go
func (store *Store) Recall(ctx context.Context, query string, limit int) (RecallResult, error)
func (store *Store) Profile(ctx context.Context) ([]Memory, error)
```

Three lanes are ranked and fused by reciprocal rank: cosine similarity over memory vectors, character-bigram overlap over the text, and cosine similarity over trigger phrases. Character bigrams let a two-character word count, which matters in Korean, where many nouns are two syllables long. The leading hit brings the rest of its origin with it. Age plays no part in ranking, because a question about last year wants last year's memory; the store keeps what is current by superseding what is not.

Every memory returned is reinforced by `1 − retrievability`, so a memory recalled just before it would have faded gains the most, and one recalled a minute after it was written gains nothing. Without an embedder, or when embedding fails, recall answers from the text lane and says why in `DegradedReason`.

`Profile` returns the live static memories, most useful first.

## Forget

Deletes what a person asked to forget, and asks when the request is ambiguous.

```go
func (store *Store) Forget(ctx context.Context, target string) (ForgetOutcome, error)
func (store *Store) ForgetMemories(ctx context.Context, memoryIDs []string, requestPhrase string) ([]Memory, error)
```

`Forget` resolves the phrase in order: an exact memory identifier, then an exact sentence, then a phrase exactly one live memory contains. A single match is deleted. Several matches, or none, come back as `Candidates` and nothing is deleted; the caller shows them to the person and passes the chosen identifiers to `ForgetMemories`. Every deletion leaves a tombstone that records the phrase.

## Sweep

Clears room, and only when there is no room left.

```go
func (store *Store) Sweep(ctx context.Context) (SweepReport, error)
```

1. Memories past their expiry go cold.
2. Static memories beyond `StaticShare` of `Capacity` lose the static flag, weakest first. They stay in the store.
3. While live memories exceed `Capacity`, the least useful origins go cold together. An origin that holds a static memory within its quota is left alone.
4. While stored memories exceed `Capacity`, cold memories older than `ColdGrace` are deleted: superseded first, then expired, then pressure, least useful first within each.
5. Tombstones beyond `TombstoneCapacity` are pruned, oldest first.

A sweep with headroom deletes nothing, however old or cold its memories are. Deleting when there is room is pure loss.

# Ports

## Embedder

Turns text into vectors for the vector and trigger lanes.

```go
type Embedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}
```

Every stored vector records `EmbeddingModel`. Vectors from another model never rank; `Reembed` moves memories and trigger phrases onto the current model in batches, and until it runs those memories answer through the text lane.

## LanguageModel

Produces structured output for decomposition and rehearsal.

```go
type LanguageModel interface {
	GenerateStructured(ctx context.Context, request StructuredRequest) (string, error)
}
```

The request carries a schema name, a JSON schema, an instruction and the subject. bluememo owns the prompts and schemas (`DecompositionInstruction`, `DecompositionSchemaDocument`, `TriggerInstruction`, `TriggerSchemaDocument`); the host only moves them to a model and returns the JSON it produced.

## Judge

Decides how a new sentence relates to the memories already held.

```go
type Judge interface {
	Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error)
}
```

`DistributionJudge` is the implementation bluememo ships. It asks up to three closed questions through a `Chooser` and reads the probability of each single-digit answer: how much the sentence is worth (1 to 5), how it relates to the candidates, and which candidate that is. A rating of 1 is noise and the sentence is dropped. The relation is only asked when there are candidates. When the leading answer is not ahead of the runner-up by 0.10, or by 0.25 for `same`, the verdict falls back to `unrelated`, which keeps both sentences. Keeping two sentences apart can be fixed later; merging them loses one.

```go
type Chooser interface {
	Choose(ctx context.Context, request ChoiceRequest) (map[string]float64, error)
}
```

A host that already has a decision model returning answer probabilities adapts it to `Chooser` in a few lines. `ollama.Client` implements it with the log probabilities Ollama returns.

## EntityResolver

Links a sentence to the people it names, only when the name is unambiguous.

```go
type EntityResolver interface {
	Resolve(content string) (resolvedEntityIDs []string, unresolvedNames []string)
}
```

`PeopleRegistry` resolves a name only when exactly one person answers to it, and reports the rest as unresolved so an ambiguous name is visible instead of silently empty. When the directory changes, `ResolveEntitiesAgain` recomputes every memory. A memory whose name stayed unresolved is still found by its words.

# Configuration

Every field of `Configuration`, and what happens when it is left empty.

| field | default | effect |
| --- | --- | --- |
| `Embedder` | none | without it, recall answers from text only and settling fails with `ErrNoEmbedder` |
| `EmbeddingModel` | `""` | the name stamped on every vector; vectors with another name do not rank |
| `Model` | none | required for settling (`ErrNoLanguageModel`) |
| `Judge` | none | required for settling (`ErrNoJudge`) |
| `People` | none | without it, no names are resolved |
| `Capacity` | 10,000 | live memories kept before eviction starts |
| `StaticShare` | 0.2 | share of `Capacity` static memories may take |
| `TombstoneCapacity` | `Capacity` | tombstones kept |
| `ColdGrace` | 30 days | how long a cold memory waits before it may be deleted |
| `HalfLife` | 30 days | retrievability half-life for a memory stored once |
| `ClaimDuration` | 10 minutes | how long a settling worker holds a group |
| `Location` | UTC | the time zone expiry periods end in |
| `Logger` | `slog.Default()` | where rejected sentences and failed rehearsals are reported |
| `Now` | `time.Now` | the clock, replaceable in tests |

The numeric defaults are starting points. Nothing has measured them yet.

Measured on an Apple-silicon laptop with 1,024-dimension vectors, a recall over 10,000 memories takes about 85 ms, and settling one sentence against that many costs about 30 ms before the judge's model calls.

# Q&A

**Why SQLite and not a server database?** One file per person makes the operating system the access boundary. A host that opens the file as the person it serves cannot read anyone else's memory, and there is no permission model to get wrong.

**Why does time not affect ranking?** Measured, it only hurt. Decay buried the right answer to questions about the past, and a recency bonus lifted near-duplicates over the correct older memory. Time decides what gets evicted.

**Why a separate judge instead of a similarity threshold?** A similarity score cannot tell `Alex likes apples` from `Alex likes pears more than apples`, and merging them loses a fact for good. Embeddings gather candidates; a model reads them.

**Where did the design come from?** [Issue #3](https://github.com/yeomyeonggeori/bluememo/issues/3) holds the decisions and the measurements behind each of them.
