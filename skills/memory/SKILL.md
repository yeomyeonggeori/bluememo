---
name: memory
description: Recall what this person has said before, and keep what is worth saying again. Use when they refer to something from an earlier conversation, when they state a lasting preference or fact about themselves or their work, or when something remembered turns out to be wrong.
compatibility: Requires the bluememo tool server.
---

# Memory

## Recall before answering from nothing

Search when an answer depends on something said in an earlier conversation: a preference, a name, where something lives, how they like work done. Search with the words you would ask in, not keywords.

Each line comes back as an identifier and a fact. The identifiers are what `forget` takes.

A recall may say it ranked by wording alone. That is the store without an embedder, which is the ordinary case; it means wording matters, so ask in words close to how the fact would have been written.

## Keep what will be wanted again

Remember a fact when it will still be true next week and would otherwise have to be asked for twice. A preference, a decision and its reason, where a thing is kept, how someone wants to be addressed.

Write each one so it stands on its own, naming who or what it is about. A statement that says "he prefers it" is worthless a month later.

```
good  이찬희 keeps the quarterly ledger in Numbers, not a spreadsheet
bad   he keeps it in Numbers
```

Do not remember what this conversation already carries, what is written in a file the agent can read, or anything derivable from either. Memory is for what would otherwise be lost.

## Forget what turned out wrong

Forget by the identifiers a search returned, with a reason. The reason is kept, so the same fact is not learned again from the same mistake.

Correcting a fact is forgetting the old one and remembering the new one. Leaving both makes a recall return two answers that disagree.
