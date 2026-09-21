package main

const schemaStatements = `
create table if not exists memory (
  memory_id           text primary key,
  content             text not null,
  kind                text not null check (kind in ('identity','preference','fact','episode','procedure')),
  valid_until         text,
  resolved_entity_ids text not null default '[]',
  unresolved_names    text not null default '[]',
  storage_strength    real not null default 1.0,
  retrieval_strength  real not null default 1.0,
  created_at          text not null,
  last_recalled_at    text,
  superseded_by       text references memory(memory_id),
  forgotten_at        text
);

create index if not exists memory_live on memory(kind)
  where superseded_by is null and forgotten_at is null;

create virtual table if not exists memory_search using fts5(
  content, memory_id unindexed, tokenize = 'trigram'
);

create table if not exists memory_vector (
  memory_id text primary key references memory(memory_id),
  vector    blob not null
);

create table if not exists tombstone (
  memory_id  text primary key,
  content    text not null,
  kind       text not null,
  reason     text not null,
  died_at    text not null
);
`
