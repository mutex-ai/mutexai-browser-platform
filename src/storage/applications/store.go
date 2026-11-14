package applications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Store uses the process's shared PostgreSQL pool; it owns no connections.
type Store struct{ db *sql.DB }

// NewStore wires the global app registry.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create inserts a normalized definition with retry-safe creation metadata.
func (s *Store) Create(ctx context.Context, key string, in Definition) (Record, bool, error) {
	if len(in.body) == 0 || key == "" {
		return Record{}, false, ErrInvalid
	}
	for range 5 {
		var random [6]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Record{}, false, fmt.Errorf("generate application ID: %w", err)
		}
		record, replayed, err := s.create(ctx, key, hex.EncodeToString(random[:]), in)
		if !errors.Is(err, errIDCollision) {
			return record, replayed, err
		}
	}
	return Record{}, false, errIDCollision
}

func (s *Store) create(ctx context.Context, key, proposedID string, in Definition) (Record, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	keyHash := sha256.Sum256([]byte(key))
	now := time.Now().UTC()
	var id string
	var previousHash []byte
	// Let any unique conflict settle first. A concurrent retry may collide on
	// both its request key and slug, so targeting only one index is insufficient.
	err = tx.QueryRowContext(ctx, `INSERT INTO platform.applications
	 (app_id,definition,created_ts_utc,updated_ts_utc,creation_request_key,creation_request_hash)
	 VALUES($1,$2,$3,$3,$4,$5)
	 ON CONFLICT DO NOTHING RETURNING app_id`, proposedID, string(in.body), now, keyHash[:], in.hash[:]).Scan(&id)
	replayed := errors.Is(err, sql.ErrNoRows)
	if replayed {
		err = tx.QueryRowContext(ctx, `SELECT app_id,creation_request_hash FROM platform.applications WHERE creation_request_key=$1`, keyHash[:]).Scan(&id, &previousHash)
		if errors.Is(err, sql.ErrNoRows) {
			var collision bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform.applications WHERE app_id=$1)`, proposedID).Scan(&collision); err != nil {
				return Record{}, false, err
			}
			if collision {
				return Record{}, false, errIDCollision
			}
			return Record{}, false, ErrSlugConflict
		}
		if err == nil && !bytes.Equal(previousHash, in.hash[:]) {
			return Record{}, false, ErrRequestConflict
		}
	}
	if err != nil {
		return Record{}, false, constraintError(err)
	}
	if !replayed {
		if err := checkDependencies(ctx, tx, id, in.dependencies); err != nil {
			return Record{}, false, err
		}
		if err := replaceHosts(ctx, tx, id, in.hosts); err != nil {
			return Record{}, false, err
		}
	}
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+" WHERE a.app_id=$1", id))
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, err
	}
	return record, replayed, nil
}

// Update replaces a definition and its host claims atomically. PostgreSQL's
// revision predicate prevents lost edits between replicas; Go validates input.
func (s *Store) Update(ctx context.Context, id string, expected int64, in Definition) (Record, error) {
	if !ValidID(id) || expected < 1 || len(in.body) == 0 {
		return Record{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkDependencies(ctx, tx, id, in.dependencies); err != nil {
		return Record{}, err
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `UPDATE platform.applications
	 SET definition=$1,revision=revision+1,updated_ts_utc=$2
	 WHERE app_id=$3 AND revision=$4 RETURNING revision`, string(in.body), time.Now().UTC(), id, expected).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform.applications WHERE app_id=$1)`, id).Scan(&exists); err != nil {
			return Record{}, err
		}
		if !exists {
			return Record{}, ErrNotFound
		}
		return Record{}, ErrRevisionConflict
	}
	if err != nil {
		return Record{}, constraintError(err)
	}
	if err := replaceHosts(ctx, tx, id, in.hosts); err != nil {
		return Record{}, err
	}
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+" WHERE a.app_id=$1", id))
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Get reads one app, including its host claims, from one database snapshot.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, ErrInvalid
	}
	return scanRecord(s.db.QueryRowContext(ctx, recordSelect+" WHERE a.app_id=$1", id))
}

// List returns the complete global catalog, including identity records. One SQL
// statement keeps each returned definition and its hosts consistent during edits.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, recordSelect+" ORDER BY a.app_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []Record{}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

const recordSelect = `SELECT a.app_id,a.revision,a.definition,a.created_ts_utc,a.updated_ts_utc,
 COALESCE((SELECT jsonb_agg(h.host_pattern ORDER BY h.host_pattern)
 FROM platform.application_hosts h WHERE h.app_id=a.app_id),'[]'::jsonb)
 FROM platform.applications a`

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var record Record
	var body, hosts []byte
	if err := row.Scan(&record.ID, &record.Revision, &body, &record.CreatedTSUTC, &record.UpdatedTSUTC, &hosts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return Record{}, fmt.Errorf("decode stored application: %w", err)
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return Record{}, errors.New("stored application is not an object")
	}
	profile, _ := doc["profile"].(map[string]any)
	urls, _ := profile["urls"].(map[string]any)
	meta, _ := doc["meta"].(map[string]any)
	if urls == nil || meta == nil {
		return Record{}, errors.New("stored application has no profile or metadata")
	}
	var claims []string
	if err := json.Unmarshal(hosts, &claims); err != nil {
		return Record{}, fmt.Errorf("decode stored host claims: %w", err)
	}
	urls["hosts"] = claims
	doc["id"] = record.ID
	meta["revision"] = record.Revision
	record.App, err = json.Marshal(doc)
	if err != nil {
		return Record{}, fmt.Errorf("encode stored application: %w", err)
	}
	record.CreatedTSUTC = record.CreatedTSUTC.UTC()
	record.UpdatedTSUTC = record.UpdatedTSUTC.UTC()
	return record, nil
}

func replaceHosts(ctx context.Context, tx *sql.Tx, id string, hosts []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform.application_hosts WHERE app_id=$1`, id); err != nil {
		return err
	}
	if len(hosts) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO platform.application_hosts(host_pattern,app_id)
	 SELECT host,$1 FROM unnest($2::text[]) AS host`, id, hosts)
	return constraintError(err)
}

func checkDependencies(ctx context.Context, tx *sql.Tx, id string, dependencies []string) error {
	if slices.Contains(dependencies, id) {
		return ErrDependency
	}
	if len(dependencies) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.applications WHERE app_id=ANY($1::text[])`, dependencies).Scan(&count); err != nil {
		return err
	}
	if count != len(dependencies) {
		return ErrDependency
	}
	return nil
}

func constraintError(err error) error {
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) || postgres.Code != "23505" {
		return err
	}
	switch postgres.ConstraintName {
	case "applications_slug_key":
		return ErrSlugConflict
	case "application_hosts_pkey":
		return ErrHostConflict
	case "applications_pkey":
		return errIDCollision
	default:
		return err
	}
}
