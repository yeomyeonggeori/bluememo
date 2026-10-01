create table file_with_code (
  file_id         text primary key,
  name            text not null check (length(name) > 0),
  extension       text not null,
  kind            text not null check (kind in ('document', 'image', 'video', 'audio', 'other')),
  summary         text not null check (length(summary) > 0),
  data            text not null default '{}' check (json_valid(data)),
  category        text not null default '' check (length(category) <= 4),
  supersedes      text references file_with_code (file_id),
  embedding_model text not null default '',
  embedding       blob,
  created_at      integer not null
);

insert into file_with_code
  select file_id, name, extension, kind, summary, data, substr(category, 1, 4),
         supersedes, embedding_model, embedding, created_at
  from file;

drop table file;

alter table file_with_code rename to file;

create index file_by_category on file (category) where category <> '';
create unique index file_supersedes_once on file (supersedes) where supersedes is not null;
