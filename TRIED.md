# Tried

What was measured against this store and rejected, so nobody measures it twice.

An entry belongs here when a change was built far enough to score and then not
kept. It records the hypothesis, what the numbers said, and which part of the
hypothesis was wrong. A change that shipped is in the history; this file is for
the ones that did not, because their absence from the code is otherwise
indistinguishable from nobody having thought of them.

The evaluation sets are `recall_eval_test.go` with `testdata/recall-eval.json`
and `judge_eval_test.go` with `testdata/judge-eval.json`. Live runs use
`perplexity/pplx-embed-v1-4b` for embedding and `~typesafe/jev-latest` through
OpenRouter's decisions endpoint for judging and reranking. The recall baseline
without a reranker is overall recall@1 0.804, recall@3 0.967, MRR 0.931, evolve
0.833, holdout 0.750.

## Recency in ranking

Rejected before this file existed, in issue #3. A blanket decay, an always-on
recency bonus and a query-conditional bonus all scored 9/10 against plain
similarity's 9/10, and the two that moved old memories broke questions that ask
about the past: `exp(-400/90)` zeroes a 400-day-old episode that "채용 얘기
누구랑 했었지?" wants. The one configuration reaching 10/10 used a hand-labelled
oracle, and an embedding probe in its place fell back to 9/10. **Time does not
enter ranking.**

## Five changes measured on reused LoCoMo stores, one kept

Recall, answering and grading run against a store that is already ingested, so these
five share one set of stores and differ only in the change under test. The spread is two
runs of the same arm. **The answering and grading models alone move overall accuracy by
about 0.024 between runs**, which is four times the spread of a full re-ingest and is the
floor every verdict below is measured against. A single run cannot see a change this size.

| change | overall | verdict |
| --- | --- | --- |
| recall limit 20 to 50 | 0.626 to **0.669** | kept; real at 3.6x the floor |
| every memory carries a time, `coalesce(occurred_at, created_at)` | 0.653 to 0.655 | dropped; temporal fell 0.643 to 0.603 |
| sibling expansion seeded from the top 3 hits, 2 per origin | 0.669 to 0.658 | dropped; open-domain fell 0.474 to 0.395 |
| sibling expansion removed entirely | 0.669 to 0.658 | dropped; single-hop fell 0.746 to 0.728, real at 4.5x |
| the date taken out of the embedded text | 0.669 to 0.652 | dropped; single-hop fell 0.746 to 0.721, real at 6.5x |

### The recall limit was starving the questions that need several facts

Multi-hop went 0.443 to 0.557 when the limit went 20 to 50, the largest single move
measured on this benchmark. Sweeping to 100 bought nothing more (0.671 against 0.669,
inside the floor) and cost adversarial decline 0.881 to 0.858: past some point extra
context only persuades the answerer to assert. 50 is where the curve flattens and before
the harm starts, so `DefaultRecallLimit` is 50 rather than 12.

Hindsight spends a token budget here rather than a count (`fact_budget.py`). A count is
the simpler unit and works because a bluememo fact is capped at 240 characters, so the
count bounds the tokens. Hindsight's facts are narrative and vary in length, which is why
it has to count tokens.

### How much of the store carries a date is a tuned quantity, and it is already tuned

Three points, same stores, same limit:

| memories whose embedded text carries a date | overall |
| --- | --- |
| none | 0.652 |
| only those with a real occurrence, about a fifth | **0.669** |
| all of them, falling back to when they were mentioned | 0.655, and temporal 0.603 |

A sparse, genuine date is a discriminator. Put it on everything and a memory *mentioned*
in June competes with one that *happened* in June, which is the same failure as the
rejected entity lane: a key that every row carries says nothing. Take it off everything
and near-duplicates collide again — the loss shows up in **single-hop**, not temporal, so
the date is separating similar memories rather than answering questions about time.

### Sibling expansion earns its place, and the limit is why seeding did not

Seeding more hits was meant to fix a squeeze: expansion inserts a bundle ahead of ranked
hits and the tail is then truncated. Raising the limit removed the squeeze, so the
mechanism had nothing left to buy. Removing expansion altogether costs single-hop 0.018,
real at 4.5 times the floor, so it stays exactly as it is. A number replaced a mechanism;
the mechanism it replaced was the one proposed, not the one already there.

## Running the LoCoMo benchmark

`locomo_accuracy_test.go` ingests LoCoMo conversations, answers its questions from
recalled memories and grades the answers. It skips unless its variables are set.

The dataset is not in the repository. Fetch `locomo10.json` (2.7 MB, 10 conversations,
1986 questions) from `https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json`.

| variable | meaning |
| --- | --- |
| `BLUEMEMO_LOCOMO_DATASET` | path to `locomo10.json` |
| `BLUEMEMO_LOCOMO_OUTPUT` | directory for the store, the log and the result JSON |
| `BLUEMEMO_LOCOMO_CONVERSATION` | index 0 to 9; one process per conversation |
| `BLUEMEMO_LOCOMO_SESSION_LIMIT` | sessions to ingest; a question whose evidence lies beyond it is not scored |
| `BLUEMEMO_LOCOMO_ARM` | a name for the run, used in the result filename |
| `BLUEMEMO_LOCOMO_TURN_WINDOW` | turns per note; unset means one note per session |

It also needs the embedder, reranker and answerer variables the other live evals use.
Ten conversations at a session limit of 10 take about 25 minutes when the ten run at
once, and cost about a dollar. Turn-level ingestion does not fit that budget: the
decompose calls are rate-limit bound, and three attempts stalled for around twenty
minutes each.

Score categories 1 to 4 on correctness and category 5 on whether the answer declines,
and report them separately. Published baselines grade categories 1 to 4.

Run each arm twice. The within-arm spread on this benchmark is 0.026 overall and up to
0.079 on open-domain, so a single run cannot separate a small gain from noise.

## An occurrence-range filter

Branch `feat/recall-within-an-occurrence-range`. The hypothesis was that the
other use issue #3 keeps `occurred_at` for, range queries, was missing: a caller
who knows the period cannot ask for it. Time selected candidates and similarity
ranked them, so no recency score was involved.

Overall recall@1 did not move, 0.804 either way. Two cells moved in opposite
directions: `temporal-04` rose from rank 2 to 1 and `temporal-06` fell from 1 to
2, so evolve read 0.833 to 0.867 while **holdout read 0.750 to 0.688**. The
feature traded a held-out case for a development case. The probable mechanism is
its own consistency rule: restricting the origin bundle too, so context that was
lifting the right answer fell out of range with everything else.

An earlier reading of the same branch measured +0.25 on `temporal` recall@1 and
was worthless. Every distractor in the temporal cases was undated, so any range
excluded six of eight candidates for free. Dating them halved the gain.

**Untested:** a memory whose date reached `occurred_at` but never its prose, as
"어제 박예시랑 배포 얘기했다" becomes. A question about that week has no lexical
hook.

## Time as the signal separating recurring events

Six `periodic` cases, each an event recurring across four or more periods with
the question naming one. The hypothesis was that similarity cannot tell the
occurrences apart, so time is the only signal that can.

All six retrieved at rank 1 with no help at all. **A recurring event names its
own period in its own text**: "6월 정산 회의" and "7월 정산 회의" differ by one
character and the character-bigram lane separates them. The cases stay in the
fixture, saturated at 1.000, to record that this ground is covered.

## An entity lane keyed on a person

Branch `feat/entity-lane`. `resolved_entity_ids` is written at settle time and
read by nothing, and `cross_note` is the weakest category, failing in one shape:
a case needs two sentences and gets one at the top. The leading hit's entity
neighbours were pulled in the way `withSiblings` pulls its origin bundle.

`cross_note` recall@1 stayed at 0.375 while its recall@3 fell from 0.812 to
0.750 and overall recall@3 from 0.967 to 0.957. On the deterministic stack
`cross_note` recall@3 fell from 0.750 to 0.562. No case newly reached the top
ranks.

**The key was the fault, not the mechanism.** Three people appear in nearly
every memory of the fixture, so sharing an entity with the leading hit means
little more than naming the same person, and the promoted neighbours were the
ones already at ranks two to five. A narrower key is issue #3's `topic` axis,
which is specified there and not built.

## Julia-1 as the judge

Disqualified in #13. It took 3 of 23 at-risk cases cold, twice returning `same`
at margins of 0.722 and 0.777, far above the 0.25 the gate reserves for that
relation, so nothing stopped it. Accuracy was 0.250. Its distribution is
genuinely graded and its confidence does not track correctness, which is worse
than being uncalibrated in one direction. It answered a shortened question
because its 256-token budget for question plus options cannot hold the full
instruction; the handicap is recorded, and the margin failures are not
explained by it.

## Julia-1 as the reranker

Worse than no reranking in both modes: overall recall@1 0.228 pointwise and
0.522 listwise against a 0.804 baseline. Two `periodic` cases exceed its budget
even at a depth of ten, so the listwise figure is a partial run. Its answer also
moves with the option count: on one case it prefers the May meeting to the July
one the question names at three through seven options, and the July one at eight
through ten.

## Jev pointwise as the reranker

Beaten by Jev listwise and not kept. Pointwise took overall recall@1 to 0.837
and holdout to 0.781, but **regressed `update` from 1.000 to 0.875** and broke
`temporal-07`, `cross-01` and `cross-02`, because rescoring every candidate
alone reshuffles ranks that were already right. Listwise reached 0.880 and 0.844
with no category regressing, at a fifth of the cost. A pointwise scorer cannot
say which of two topically relevant memories answers the question.

## A cosine threshold as the read-time relevance signal

`RecallResult.Relevance` carries the closest cosine, because the vector lane
computed it and threw it away. The hypothesis was that exposing it would let a
caller decline when nothing relevant was found, and so escape a trade-off
measured on LoCoMo: instructing the answering model to decline when unsure
scores 0.404 on the 1,018 answerable questions and declines 0.922 of the 446
unanswerable ones, while instructing it to answer what the memories imply
scores 0.474 and 0.482. Seven points on one side cost forty-four on the other.

The signal is real. The fused RRF top score medians 0.0458 against 0.0443 and
separates the two sets with an AUC of 0.604; the closest cosine medians 0.6321
against 0.5325 and separates them at 0.772.

The threshold is not. Declining below it and answering above it was simulated
across the whole set at every threshold from 0.50 to 0.62, and the best lands
at 0.541 against the 0.561 of the decline-when-unsure instruction. The field
shipped anyway, for deciding whether to spend a model call, but the
accuracy claim did not.

What was wrong: a model reading the retrieved memories judges their relevance
better than their geometry does. The abstention gap is real and a threshold
does not close it.

## Where LoCoMo says the loss actually is

The first measurement on a benchmark with published numbers. Ten conversations,
the first twenty sessions of each, one note per session, 1,464 questions of
which 446 are unanswerable. Hindsight reports 89.61 and Backboard claims 90.00
on this set. bluememo's best whole-set score was 0.561.

Splitting the 600 wrong answers by where the gold answer's terms appear, which
needs no model calls, only a query over each store:

| the gold fact was | share | what that means |
| --- | --- | --- |
| in the recalled top 20 | 43% | the answering step, or the fact is present without being usable |
| in the store, outside the top 20 | 34% | ranking |
| absent from the store | 23% | the decomposer did not keep it |

Nothing was lost to forgetting, supersession or expiry: `only superseded
memories` and `only expired memories` are both 0.000 of the gold turns. Whatever
is wrong here, dedup and the forgetting curve did not cause it.

Retrieval by category, session-level evidence recall@20: multi-hop 0.670,
temporal 0.777, open-domain 0.751, single-hop 0.811. Accuracy by category:
multi-hop 0.288, temporal 0.340, open-domain 0.413, single-hop 0.591.
Multi-hop is the weakest on both. It was read as the same weakness as
`cross_note` in the Korean set, and that reading was wrong: a `cross_note` case
expects two sentences, so its recall@1 cannot exceed 0.500, and 0.438 is 87.5%
of that ceiling with recall@3 at 1.000. Both sentences are always in the top
three. LoCoMo multi-hop has no such ceiling, so 0.288 stands on its own, but the
Korean half of the argument does not.

Caveats on the number. The ingestion unit was one note per session, about
twenty-two turns of dialogue, chosen to fit a thirty-minute budget; memory
density was 0.42 per turn against 1.0 in a turn-level pilot, and relative time
words survive unresolved in the store ("Last year" against a gold answer of
"2022"). Turn-level ingestion could not be measured inside the budget: it makes
roughly ten times the decomposer calls and becomes rate-limit bound, stalling
about twenty-two minutes in every attempt. The answering model and the judge
were both `openai/gpt-6-luna`, while the published figures used GPT-OSS-120B
and GPT-4o, so the yardstick is not identical.

## Claims that were wrong

- **Jev returns a one-hot distribution.** It does not. Its relation margin ran
  from 0.110 to 1.000 across 19 distinct values, and criteria keyed by digit and
  by relation name both came back graded. The probes that looked degenerate were
  items Jev finds easy, where it saturates.
- **`choice` naming one winner makes it a poor reranker.** The opposite:
  promoting one candidate and leaving the rest in fused order is a minimal
  intervention that cannot scramble anything, which is why listwise beat
  pointwise.
- **The margin gate is what protects live memories.** On the judge fixture it
  prevented zero unsafe verdicts and cost two correct ones. What abstained was
  the target step: one case led with `updates` at a margin of 1.000 and ended
  `unrelated` only because the target question answered none.

## Iterative selection as the reranker

A `choice` question names one winner and zeroes the rest, so the merged reranker
lifts one memory and leaves the tail in fused order. The hypothesis was that a
question needing two memories wants a reranker that picks a set, so the choice
was run three times, excluding the winner each round and scoring by round.

`cross_note` recall@1 did not move, 0.438 either way, and recall@3 fell from
0.981 to 0.963 overall and from 0.875 to 0.750 on `metadata_date`. Rejected on
both clauses of its bar.

What was wrong: the premise. `cross_note` recall@1 is capped at 0.500 by the
metric and already sits at 87.5% of it, with recall@3 at 1.000, so there was no
headroom for a set-picking reranker to take. Three attempts in one session aimed
at that category before anyone worked out the ceiling.

The run was not wasted. Its table showed `metadata_date` at recall@1 0.000 with
a reranker against 0.500 without one, which is how the defect fixed in #21 was
found.

## How a measurement here has gone wrong before

Four times a harness reported a number that flattered or misdirected the change,
and each was caught by asking what would have to be true for it.

- A monotonic identifier source made ranking deterministic and handed the
  expected memories the lowest identifiers, so they won every score tie.
  Recall@3 read 0.975.
- Undated distractors let an occurrence-range filter exclude six of eight
  candidates for free.
- Recurrence cases written to need a temporal signal put the period in their own
  text, where the lexical lane already had it.
- A category whose cases expect two sentences was read as the weakest in the set
  on recall@1, when that metric cannot exceed 0.500 there. Check a category's
  ceiling before calling it weak.

A fifth kind is worth separating, because it is not flattery. The reranker was
merged on the 46-case set, where no question's date lived only in metadata. Once
those eight cases existed it scored worse overall than no reranker at all. A
change measured on a set that cannot see its failure mode reads as safe.

Ranking ties break on the memory identifier, which `NewIdentifier` draws from
`crypto/rand`, so a fresh store per case orders tied memories differently on
every run. The harnesses inject a fixed-seed source. Five `-count=1` runs
agreeing exactly is the check that this has not come back.
