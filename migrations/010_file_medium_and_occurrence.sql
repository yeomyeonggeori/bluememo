create table file_with_occurrence (
  file_id         text primary key,
  name            text not null check (length(name) > 0),
  extension       text not null,
  medium          text not null check (medium in ('document', 'image', 'video', 'audio', 'other')),
  summary         text not null check (length(summary) > 0),
  data            text not null default '{}' check (json_valid(data)),
  category        text not null default ''
                    check (length(category) <= 4 and category not glob '*[^0-9A-Za-z]*'),
  occurred_at     integer,
  occurred_until  integer,
  supersedes      text references file_with_occurrence (file_id),
  embedding_model text not null default '',
  embedding       blob,
  created_at      integer not null,
  check (occurred_until is null or occurred_at is not null)
);

insert into file_with_occurrence (file_id, name, extension, medium, summary, data, category,
                                  supersedes, embedding_model, embedding, created_at)
  select file_id, name, extension, kind, summary, data, category,
         supersedes, embedding_model, embedding, created_at
  from file;

drop table file;

alter table file_with_occurrence rename to file;

create index file_by_category on file (category) where category <> '';
create index file_by_occurrence on file (occurred_at) where occurred_at is not null;
create unique index file_supersedes_once on file (supersedes) where supersedes is not null;
