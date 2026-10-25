-- +goose Up
-- Record the node that executed each task. After failover, the catalogue moves to a different writer; historical jobs
-- may have run elsewhere, so record the node at creation time. Legacy tasks retain a blank value.
ALTER TABLE tasks ADD COLUMN node TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tasks DROP COLUMN node;
