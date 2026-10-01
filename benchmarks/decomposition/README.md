# Does a gold answer survive decomposition

A fact that is vaguer than the text it came from cannot answer a question about
what the text said, and no amount of retrieval recovers it. This measures that
directly and needs no answering and no judge, so a change to
`DecompositionInstruction` can be tried for the cost of one ingestion.

For every LoCoMo question whose answer appears in the conversation, it asks
whether the answer also appears somewhere in the facts that conversation settled
into. Questions whose answer is not in the conversation at all are counted
separately and left out of the rate, since nothing decomposition does could keep
them.

```bash
python3 benchmarks/decomposition/survival.py <store.db> locomo10.json conv-26
```

It prints the survival rate and the first few answers that did not make it,
which is where the next rule comes from: reading them is how the rule about a
statement never being vaguer than its text was written.

The comparison is between two stores built from one conversation under two
instructions. Build each with `cmd/bench-server` and the documents for that
conversation, which is ingestion alone: no answer model, no judge, no benchmark.

Word overlap is the test, so it is loose in both directions. An answer whose
words appear in an unrelated fact counts as survived, and a fact that carries
the meaning in other words counts as lost. It is useful because the same bias
applies to both sides of a comparison, and useless as an absolute number.

## What this cannot see

Survival asks whether the words of a gold answer are anywhere in the
store. It does not ask whether the memories a question retrieves let a
model answer it, and the two came apart badly once:

| decomposition | memories | survival | accuracy |
| --- | --- | --- | --- |
| forbid a vaguer statement | 267 | 0.810 | 0.8750 |
| the three losses named | 332 | 0.912 | 0.8684 |
| said before propositions | 483 | 0.971 | 0.8289 |

Splitting one statement into two scatters the same words across more
memories. Coverage rises by arithmetic while each piece carries less,
and a recall of fifty returns statements, not stores: the fact ranks
and its qualifier does not. A change that raises survival by making
more, smaller statements has not been shown to help anything until it
is run through AMB.
