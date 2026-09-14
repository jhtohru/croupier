CREATE TABLE inbox (
    consumer_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    payload_hash BYTEA NOT NULL,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (consumer_name, message_id)
);
