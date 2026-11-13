-- Platform owns this schema in its dedicated database.
-- Customer tables will be introduced by subsequent migrations.
-- +goose Up
CREATE SCHEMA platform;

-- +goose Down
DROP SCHEMA platform;
