create index pending_note_by_origin on pending_note (origin_id) where origin_id is not null;
