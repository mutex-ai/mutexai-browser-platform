-- +goose Up
CREATE TABLE platform.id_counters (
    scope text PRIMARY KEY,
    next_id integer NOT NULL CHECK (next_id BETWEEN 1000 AND 10000)
);
INSERT INTO platform.id_counters(scope, next_id) VALUES ('partners', 1000);

CREATE TABLE platform.partners (
    partner_id integer PRIMARY KEY CHECK (partner_id BETWEEN 1000 AND 9999),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    enabled boolean NOT NULL DEFAULT true,
    console_realm_id text,
    sso_realm_id text,
    created_ts_utc timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE platform.clients (
    partner_id integer NOT NULL REFERENCES platform.partners(partner_id),
    client_id integer NOT NULL CHECK (client_id BETWEEN 1000 AND 9999),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    organization_id text,
    PRIMARY KEY(partner_id, client_id)
);
CREATE TABLE platform.sites (
    partner_id integer NOT NULL,
    client_id integer NOT NULL,
    site_id integer NOT NULL CHECK (site_id BETWEEN 1000 AND 9999),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    PRIMARY KEY(partner_id, client_id, site_id),
    FOREIGN KEY(partner_id, client_id) REFERENCES platform.clients(partner_id, client_id)
);
CREATE TABLE platform.console_domains (
    domain text PRIMARY KEY CHECK (domain = lower(domain) AND length(domain) BETWEEN 1 AND 253),
    partner_id integer NOT NULL REFERENCES platform.partners(partner_id),
    active boolean NOT NULL DEFAULT false
);
CREATE INDEX console_domains_partner ON platform.console_domains(partner_id);
CREATE TABLE platform.partner_setup (
    partner_id integer PRIMARY KEY REFERENCES platform.partners(partner_id),
    setup_id text NOT NULL UNIQUE,
    request_key bytea NOT NULL UNIQUE,
    request_hash bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed', 'complete')),
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_ts_utc timestamptz NOT NULL DEFAULT now(),
    lease_token text,
    lease_expires_ts_utc timestamptz,
    last_error text,
    updated_ts_utc timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_setup_due ON platform.partner_setup(next_attempt_ts_utc) WHERE status <> 'complete';
CREATE TABLE platform.changes (
    change_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    partner_id integer NOT NULL REFERENCES platform.partners(partner_id),
    event text NOT NULL,
    source text NOT NULL,
    created_ts_utc timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE platform.changes;
DROP TABLE platform.partner_setup;
DROP TABLE platform.console_domains;
DROP TABLE platform.sites;
DROP TABLE platform.clients;
DROP TABLE platform.partners;
DROP TABLE platform.id_counters;
