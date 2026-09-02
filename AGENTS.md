# AGENTS.md

- bluememo depends on nothing but the standard library and `pgx`. A host adapts its own task, model and identity types at the edge; do not import a host here.
- The model judges meaning (what a fact says, its kind, what it replaces); the runtime supplies facts (candidates, scopes, labels, time) and refuses what it did not offer. Never merge facts by a similarity threshold.
- The reader filter is one SQL predicate in `postgres/fact_repository.go` and one Go function in `reader.go`; keep them saying the same thing and cover both in tests.
- `migrations/` is the schema of record. Add the next file; never edit an applied one.
- Tests that need Postgres read `BLUEMEMO_TEST_POSTGRES_URL` and skip without it; CI provides `pgvector/pgvector:pg16`. Run them on a plain `postgres:16` too when touching search, because a host without the extension answers lexically.
- No comments in code, full names, gofmt clean.
