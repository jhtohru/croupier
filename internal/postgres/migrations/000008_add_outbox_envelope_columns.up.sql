-- Challenge spec §11: "O envelope deve conter eventId, eventType,
-- aggregateId, correlationId, causationId opcional, occurredAt, version e
-- data tipado." version/correlation_id/causation_id were missing from the
-- original outbox schema (migration 000005) — added here rather than
-- editing that migration, since applied migrations are never edited.
ALTER TABLE outbox
    ADD COLUMN version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN correlation_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN causation_id TEXT NULL;
