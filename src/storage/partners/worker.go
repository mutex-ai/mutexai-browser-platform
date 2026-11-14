package partners

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
)

// Worker runs one setup at a time; PostgreSQL leases coordinate other replicas.
type Worker struct {
	store       *Store
	provisioner *Provisioner
	log         *zap.Logger
}

// NewWorker uses the process's shared store and Keycloak client.
func NewWorker(store *Store, p *Provisioner, log *zap.Logger) *Worker {
	return &Worker{store: store, provisioner: p, log: log}
}

// Run blocks until cancellation. Every attempt is shorter than its database lease.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		claim, cancel := context.WithTimeout(ctx, 5*time.Second)
		job, err := w.store.Claim(claim)
		cancel()
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
				w.log.Warn("claim partner setup failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
		partner, err := w.store.Get(attempt, job.PartnerID)
		var ids IdentityIDs
		if err == nil {
			ids, err = w.provisioner.Provision(attempt, job, partner)
		}
		if err == nil {
			err = w.store.Complete(attempt, job, ids)
		}
		cancel()
		if err != nil {
			code := setupError(err)
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			saveErr := w.store.Fail(cleanup, job, code)
			stop()
			if saveErr != nil && !errors.Is(saveErr, ErrLeaseLost) {
				w.log.Warn("save partner setup failure failed", zap.Int("partner_id", job.PartnerID))
			}
			if ctx.Err() == nil {
				w.log.Warn("partner setup will retry", zap.Int("partner_id", job.PartnerID), zap.String("error_code", code))
			}
		}
	}
}

func setupError(err error) string {
	switch {
	case errors.Is(err, ErrInstanceUnavailable):
		return "instance_assignment_required"
	case errors.Is(err, ErrOwnership):
		return "identity_ownership_conflict"
	case errors.Is(err, ErrConfiguration):
		return "identity_configuration_conflict"
	case errors.Is(err, ErrLeaseLost):
		return "setup_lease_expired"
	}
	var api *keycloak.APIError
	if errors.As(err, &api) {
		if api.StatusCode == http.StatusUnauthorized || api.StatusCode == http.StatusForbidden {
			return "keycloak_access_denied"
		}
	}
	return "identity_setup_unavailable"
}
