-- +goose Up
-- Pool IDs were previously derived only from names (pool-<name>). Multiple nodes commonly name their data pool tank,
-- but the catalogue is replicated across all nodes. Three identically named pools collapsed into one shared record,
-- owned by the last writer, hiding the other two nodes' storage despite their disks serving clients.
--
-- Assign node-specific IDs (pool-<node>--<name>) to existing rows. Only already-persisted rows can be updated.
-- Previously hidden pools have no rows and will be registered when their nodes next boot using the existing
-- external-data-pool adoption process with the new identity format.
-- pool_disks.pool_id references pools(id) with ON DELETE CASCADE. Updating IDs temporarily makes one table
-- inconsistent regardless of which side is updated first. Defer foreign key checks until commit
-- so both parent and child IDs can be updated together.
PRAGMA defer_foreign_keys = ON;

UPDATE pool_disks
SET pool_id = 'pool-' || (SELECT server_id FROM pools WHERE pools.id = pool_disks.pool_id) || '--'
              || substr(pool_disks.pool_id, 6)
WHERE EXISTS (
  SELECT 1 FROM pools
  WHERE pools.id = pool_disks.pool_id
    AND pools.server_id <> ''
    AND pools.id NOT LIKE 'pool-' || pools.server_id || '--%'
);

UPDATE pools
SET id = 'pool-' || server_id || '--' || substr(id, 6)
WHERE server_id <> ''
  AND id NOT LIKE 'pool-' || server_id || '--%';
