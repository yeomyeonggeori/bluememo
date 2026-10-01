create table file_ascii_code (
  file_id         text primary key,
  name            text not null check (length(name) > 0),
  extension       text not null,
  kind            text not null check (kind in ('document', 'image', 'video', 'audio', 'other')),
  summary         text not null check (length(summary) > 0),
  data            text not null default '{}' check (json_valid(data)),
  category        text not null default ''
                    check (length(category) <= 4 and category not glob '*[^0-9A-Za-z]*'),
  supersedes      text references file_ascii_code (file_id),
  embedding_model text not null default '',
  embedding       blob,
  created_at      integer not null
);

insert into file_ascii_code
  select file_id, name, extension, kind, summary, data,
         case when category glob '*[^0-9A-Za-z]*' then '' else category end,
         supersedes, embedding_model, embedding, created_at
  from file;

drop table file;

alter table file_ascii_code rename to file;

create index file_by_category on file (category) where category <> '';
create unique index file_supersedes_once on file (supersedes) where supersedes is not null;
