-- +goose Up
INSERT INTO platform.id_counters(scope,next_id) VALUES ('instances',1000);
CREATE TABLE platform.instances (
    instance_id integer PRIMARY KEY CHECK (instance_id BETWEEN 1000 AND 9999),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    console_url text NOT NULL UNIQUE,
    backend_url text NOT NULL,
    accepting_clients boolean NOT NULL DEFAULT true,
    request_key bytea NOT NULL UNIQUE,
    request_hash bytea NOT NULL,
    created_ts_utc timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE platform.clients ADD COLUMN instance_id integer REFERENCES platform.instances(instance_id);
CREATE INDEX clients_instance ON platform.clients(instance_id);
-- Existing records remain unchanged; new clients require an instance at creation.
ALTER TABLE platform.clients ADD CONSTRAINT clients_require_instance CHECK (instance_id IS NOT NULL) NOT VALID;

-- +goose Down
ALTER TABLE platform.clients DROP CONSTRAINT clients_require_instance;
ALTER TABLE platform.clients DROP COLUMN instance_id;
DROP TABLE platform.instances;
DELETE FROM platform.id_counters WHERE scope='instances';
