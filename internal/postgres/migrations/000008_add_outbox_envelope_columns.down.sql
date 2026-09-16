ALTER TABLE outbox
    DROP COLUMN version,
    DROP COLUMN correlation_id,
    DROP COLUMN causation_id;
