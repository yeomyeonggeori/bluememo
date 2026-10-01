create table file (
  file_id         text primary key,
  name            text not null check (length(name) > 0),
  extension       text not null,
  kind            text not null check (kind in ('document', 'image', 'video', 'audio', 'other')),
  summary         text not null check (length(summary) > 0),
  data            text not null default '{}' check (json_valid(data)),
  category        text not null default '',
  supersedes      text references file (file_id),
  embedding_model text not null default '',
  embedding       blob,
  created_at      integer not null
);

create index file_by_category on file (category) where category <> '';
create unique index file_supersedes_once on file (supersedes) where supersedes is not null;
