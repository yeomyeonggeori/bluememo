alter table memory rename column occurred_to to occurred_until;
alter table tombstone add column occurred_until integer;
alter table memory_relation rename to memory_edge;
drop index pending_note_unsettled;
create index pending_note_by_group on pending_note (group_id) where settled_at is null;
