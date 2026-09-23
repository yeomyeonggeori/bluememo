create table memory (
  memory_id           text primary key,
  content             text not null check (length(content) > 0),
  is_static           integer not null default 0 check (is_static in (0, 1)),
  occurred_at         integer,
  valid_until         integer,
  origin_id           text not null,
  importance          integer not null default 3 check (importance between 1 and 5),
  storage_strength    real not null default 1 check (storage_strength >= 1),
  resolved_entity_ids text not null default '[]',
  unresolved_names    text not null default '[]',
  embedding_model     text not null default '',
  embedding           blob,
  created_at          integer not null,
  last_recalled_at    integer,
  cold_since          integer,
  cold_reason         text check (cold_reason in ('pressure', 'superseded', 'expired')),
  superseded_by       text,
  check ((cold_since is null) = (cold_reason is null))
);

create index memory_by_origin on memory (origin_id);
create index memory_by_cold on memory (cold_since);

create table memory_relation (
  from_memory_id text not null references memory (memory_id) on delete cascade,
  to_memory_id   text not null references memory (memory_id) on delete cascade,
  edge           text not null check (edge in ('updates', 'extends')),
  created_at     integer not null,
  primary key (from_memory_id, to_memory_id)
);

create table memory_trigger (
  trigger_id      text primary key,
  memory_id       text not null references memory (memory_id) on delete cascade,
  phrase          text not null check (length(phrase) between 1 and 80),
  embedding_model text not null,
  embedding       blob not null,
  unique (memory_id, phrase)
);

create table pending_note (
  note_id       text primary key,
  group_id      text not null,
  body          text not null check (length(body) > 0),
  speaker_name  text not null default '',
  is_explicit   integer not null default 0 check (is_explicit in (0, 1)),
  arrived_at    integer not null,
  claim_token   text,
  claimed_until integer,
  settled_at    integer
);

create index pending_note_unsettled on pending_note (group_id) where settled_at is null;

create table tombstone (
  memory_id      text primary key,
  content        text not null,
  is_static      integer not null,
  occurred_at    integer,
  origin_id      text not null,
  reason         text not null check (reason in ('asked', 'pressure', 'superseded', 'expired')),
  request_phrase text not null default '',
  created_at     integer not null,
  died_at        integer not null
);

create index tombstone_by_death on tombstone (died_at);
