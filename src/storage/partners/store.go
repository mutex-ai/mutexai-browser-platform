package partners

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
)

// Store persists the hierarchy, creation requests and setup leases.
type Store struct{ db *sql.DB }

// NewStore uses the process's shared PostgreSQL pool.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create saves the entire initial hierarchy and setup task atomically. The root
// counter lock also serializes idempotency checks, including simultaneous retries.
func (s *Store) Create(ctx context.Context, key string, in CreateInput) (Partner, bool, error) {
	if err := in.Normalize(); err != nil {
		return Partner{}, false, err
	}
	keyHash := sha256.Sum256([]byte(key))
	data, err := json.Marshal(in)
	if err != nil {
		return Partner{}, false, err
	}
	requestHash := sha256.Sum256(data)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Partner{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var next int
	if err := tx.QueryRowContext(ctx, "SELECT next_id FROM platform.id_counters WHERE scope='partners' FOR UPDATE").Scan(&next); err != nil {
		return Partner{}, false, err
	}
	var existing int
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT partner_id,request_hash FROM platform.partner_setup WHERE request_key=$1", keyHash[:]).Scan(&existing, &previous)
	if err == nil {
		if !bytes.Equal(previous, requestHash[:]) {
			return Partner{}, false, ErrRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return Partner{}, false, err
		}
		partner, err := s.Get(ctx, existing)
		return partner, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Partner{}, false, err
	}
	if next > 9999 {
		return Partner{}, false, ErrCapacity
	}
	var available bool
	if err := tx.QueryRowContext(ctx, "SELECT accepting_clients FROM platform.instances WHERE instance_id=$1 FOR SHARE", in.InstanceID).Scan(&available); errors.Is(err, sql.ErrNoRows) {
		return Partner{}, false, ErrInstanceUnavailable
	} else if err != nil {
		return Partner{}, false, err
	}
	if !available {
		return Partner{}, false, ErrInstanceUnavailable
	}
	created := time.Now().UTC()
	statements := []struct {
		query string
		args  []any
	}{
		{"UPDATE platform.id_counters SET next_id=next_id+1 WHERE scope='partners'", nil},
		{"INSERT INTO platform.partners(partner_id,name,created_ts_utc) VALUES($1,$2,$3)", []any{next, in.Name, created}},
		{"INSERT INTO platform.clients(partner_id,client_id,name,instance_id) VALUES($1,1000,$2,$3)", []any{next, in.ClientName, in.InstanceID}},
		{"INSERT INTO platform.sites(partner_id,client_id,site_id,name) VALUES($1,1000,1000,$2)", []any{next, in.SiteName}},
		{"INSERT INTO platform.id_counters(scope,next_id) VALUES($1,1001),($2,1001)", []any{fmt.Sprintf("clients/%d", next), fmt.Sprintf("sites/%d/1000", next)}},
		{"INSERT INTO platform.partner_setup(partner_id,setup_id,request_key,request_hash,updated_ts_utc) VALUES($1,$2,$3,$4,$5)", []any{next, rand.Text(), keyHash[:], requestHash[:], created}},
		{"INSERT INTO platform.changes(partner_id,event,source,created_ts_utc) VALUES($1,'partner.created','operator-api',$2)", []any{next, created}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return Partner{}, false, err
		}
	}
	for _, domain := range in.ConsoleDomains {
		if _, err := tx.ExecContext(ctx, "INSERT INTO platform.console_domains(domain,partner_id) VALUES($1,$2)", domain, next); err != nil {
			var pgerr *pgconn.PgError
			if errors.As(err, &pgerr) && pgerr.Code == "23505" {
				return Partner{}, false, ErrDomainConflict
			}
			return Partner{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Partner{}, false, err
	}
	partner, err := s.Get(ctx, next)
	return partner, false, err
}

// Get returns the partner's initial hierarchy and current setup status.
func (s *Store) Get(ctx context.Context, id int) (Partner, error) {
	var p Partner
	var retry sql.NullTime
	var domains []byte
	err := s.db.QueryRowContext(ctx, `SELECT p.partner_id,p.name,p.enabled,p.created_ts_utc,c.client_id,c.name,s.site_id,s.name,
        COALESCE(i.instance_id,0),COALESCE(i.console_url,''),COALESCE(i.backend_url,''),
        j.status,COALESCE(j.last_error,''),CASE WHEN j.status='failed' THEN j.next_attempt_ts_utc END,
        COALESCE((SELECT json_agg(d.domain ORDER BY d.domain) FROM platform.console_domains d WHERE d.partner_id=p.partner_id),'[]'::json)
        FROM platform.partners p JOIN platform.clients c ON c.partner_id=p.partner_id AND c.client_id=1000
        JOIN platform.sites s ON s.partner_id=c.partner_id AND s.client_id=c.client_id AND s.site_id=1000
        LEFT JOIN platform.instances i ON i.instance_id=c.instance_id
        JOIN platform.partner_setup j ON j.partner_id=p.partner_id WHERE p.partner_id=$1`, id).Scan(
		&p.PartnerID, &p.Name, &p.Enabled, &p.CreatedTSUTC, &p.ClientID, &p.ClientName, &p.SiteID, &p.SiteName, &p.InstanceID, &p.ConsoleURL, &p.BackendURL, &p.SetupStatus, &p.SetupError, &retry, &domains)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(domains, &p.ConsoleDomains); err != nil {
		return p, err
	}
	p.CreatedTSUTC = p.CreatedTSUTC.UTC()
	if retry.Valid {
		value := retry.Time.UTC()
		p.RetryTSUTC = &value
	}
	if p.InstanceID == 0 {
		p.SetupStatus = "assignment_required"
		p.SetupError = "instance_assignment_required"
	}
	if p.InstanceID != 0 {
		p.ConsoleClientID = instances.ConsoleClientID(p.InstanceID)
		p.ConsoleJWKSURL = instances.ConsoleJWKSURL(p.ConsoleURL)
	}
	p.ConsoleRealm = strconv.Itoa(id)
	p.SSORealm = p.ConsoleRealm + "-sso"
	return p, nil
}

// Claim obtains one minute of exclusive setup ownership without holding a DB
// connection during Keycloak calls. Expired work is eligible after a restart.
func (s *Store) Claim(ctx context.Context) (Job, error) {
	job := Job{LeaseToken: rand.Text()}
	err := s.db.QueryRowContext(ctx, `WITH due AS (
        SELECT partner_id FROM platform.partner_setup j WHERE status<>'complete' AND next_attempt_ts_utc<=now()
        AND EXISTS (SELECT 1 FROM platform.clients c WHERE c.partner_id=j.partner_id AND c.client_id=1000 AND c.instance_id IS NOT NULL)
        AND (lease_expires_ts_utc IS NULL OR lease_expires_ts_utc<=now()) ORDER BY next_attempt_ts_utc,partner_id FOR UPDATE SKIP LOCKED LIMIT 1
        ) UPDATE platform.partner_setup j SET status='running',attempts=attempts+1,lease_token=$1,
        lease_expires_ts_utc=now()+interval '60 seconds',updated_ts_utc=$2 FROM due WHERE j.partner_id=due.partner_id
        RETURNING j.partner_id,j.setup_id,j.attempts`, job.LeaseToken, time.Now().UTC()).Scan(&job.PartnerID, &job.SetupID, &job.Attempts)
	return job, err
}

// Complete saves all identity references and completion in the same transaction.
func (s *Store) Complete(ctx context.Context, job Job, ids IdentityIDs) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE platform.partner_setup SET status='complete',last_error=NULL,lease_token=NULL,
        lease_expires_ts_utc=NULL,updated_ts_utc=$3 WHERE partner_id=$1 AND lease_token=$2 AND lease_expires_ts_utc>now()`, job.PartnerID, job.LeaseToken, time.Now().UTC())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	if _, err = tx.ExecContext(ctx, "UPDATE platform.partners SET console_realm_id=$2,sso_realm_id=$3 WHERE partner_id=$1", job.PartnerID, ids.ConsoleRealmID, ids.SSORealmID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE platform.clients SET organization_id=$2 WHERE partner_id=$1 AND client_id=1000", job.PartnerID, ids.OrganizationID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO platform.changes(partner_id,event,source,created_ts_utc) VALUES($1,'partner.identity-configured','setup-worker',$2)", job.PartnerID, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// Fail releases only this attempt's lease and schedules a bounded-backoff retry.
// code is a fixed application error code, never a raw Keycloak/SQL error.
func (s *Store) Fail(ctx context.Context, job Job, code string) error {
	delay := min(300, 5*(1<<max(0, min(job.Attempts-1, 6))))
	result, err := s.db.ExecContext(ctx, `UPDATE platform.partner_setup SET status='failed',last_error=$3,
        next_attempt_ts_utc=now()+$4*interval '1 second',lease_token=NULL,lease_expires_ts_utc=NULL,updated_ts_utc=$5
        WHERE partner_id=$1 AND lease_token=$2 AND lease_expires_ts_utc>now()`, job.PartnerID, job.LeaseToken, code, delay, time.Now().UTC())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
