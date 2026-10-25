-- +goose Up
-- Treat never-connected clients as offline; the UI only distinguishes online and offline.
UPDATE terminals SET state = 'offline' WHERE state = 'unknown';

-- +goose Down
SELECT 1;
