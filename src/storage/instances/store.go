package instances

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Store records instance registrations using the shared database pool.
type Store struct{ db *sql.DB }

// NewStore uses the process's shared PostgreSQL pool.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create allocates an ID and records an existing instance, with idempotent retries.
func (s *Store) Create(ctx context.Context, key string, in CreateInput) (Instance, bool, error) {
	if err := in.Normalize(); err != nil {
		return Instance{}, false, err
	}
	keyHash := sha256.Sum256([]byte(key))
	data, err := json.Marshal(in)
	if err != nil {
		return Instance{}, false, err
	}
	requestHash := sha256.Sum256(data)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Instance{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var next int
	if err := tx.QueryRowContext(ctx, "SELECT next_id FROM platform.id_counters WHERE scope='instances' FOR UPDATE").Scan(&next); err != nil {
		return Instance{}, false, err
	}
	var id int
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT instance_id,request_hash FROM platform.instances WHERE request_key=$1", keyHash[:]).Scan(&id, &previous)
	if err == nil {
		if !bytes.Equal(previous, requestHash[:]) {
			return Instance{}, false, ErrRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return Instance{}, false, err
		}
		instance, err := s.Get(ctx, id)
		return instance, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Instance{}, false, err
	}
	if next > 9999 {
		return Instance{}, false, ErrCapacity
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform.instances(instance_id,name,console_url,backend_url,accepting_clients,request_key,request_hash,created_ts_utc) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, next, in.Name, in.ConsoleURL, in.BackendURL, *in.AcceptingClients, keyHash[:], requestHash[:], time.Now().UTC()); err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return Instance{}, false, ErrConsoleConflict
		}
		return Instance{}, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE platform.id_counters SET next_id=next_id+1 WHERE scope='instances'"); err != nil {
		return Instance{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Instance{}, false, err
	}
	instance, err := s.Get(ctx, next)
	return instance, false, err
}

// Get returns approved addresses and the derived console registration settings.
func (s *Store) Get(ctx context.Context, id int) (Instance, error) {
	var in Instance
	err := s.db.QueryRowContext(ctx, `SELECT instance_id,name,console_url,backend_url,accepting_clients,created_ts_utc FROM platform.instances WHERE instance_id=$1`, id).Scan(&in.InstanceID, &in.Name, &in.ConsoleURL, &in.BackendURL, &in.AcceptingClients, &in.CreatedTSUTC)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.CreatedTSUTC = in.CreatedTSUTC.UTC()
	in.ConsoleClientID = ConsoleClientID(id)
	in.ConsoleJWKSURL = ConsoleJWKSURL(in.ConsoleURL)
	return in, nil
}

// SetAvailability controls new assignments without changing URLs or existing clients.
func (s *Store) SetAvailability(ctx context.Context, id int, accepting bool) (Instance, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE platform.instances SET accepting_clients=$2 WHERE instance_id=$1", id, accepting)
	if err != nil {
		return Instance{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Instance{}, err
	}
	if count != 1 {
		return Instance{}, ErrNotFound
	}
	return s.Get(ctx, id)
}
