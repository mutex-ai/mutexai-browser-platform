-- +goose Up
CREATE TABLE platform.applications (
    app_id text COLLATE "C" PRIMARY KEY,
    definition jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    created_ts_utc timestamptz NOT NULL,
    updated_ts_utc timestamptz NOT NULL,
    creation_request_key bytea NOT NULL UNIQUE,
    creation_request_hash bytea NOT NULL
);
CREATE UNIQUE INDEX applications_slug_key ON platform.applications ((definition->>'slug'));

CREATE TABLE platform.application_hosts (
    host_pattern text COLLATE "C" PRIMARY KEY,
    app_id text COLLATE "C" NOT NULL REFERENCES platform.applications(app_id)
);
CREATE INDEX application_hosts_app_id ON platform.application_hosts(app_id);

-- +goose Down
DROP TABLE platform.application_hosts;
DROP TABLE platform.applications;
