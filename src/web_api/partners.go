package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/partners"
)

// PartnerStore is the persistence boundary for the operator API.
type PartnerStore interface {
	Create(context.Context, string, partners.CreateInput) (partners.Partner, bool, error)
	Get(context.Context, int) (partners.Partner, error)
}

func partnerRoutes(r *mux.Router, store PartnerStore, log *zap.Logger) {
	r.HandleFunc("/internal/v1/partners", func(w http.ResponseWriter, r *http.Request) {
		key, ok := creationKey(w, r)
		if !ok {
			return
		}
		var in partners.CreateInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.Normalize(); err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		p, replayed, err := store.Create(ctx, key, in)
		if err != nil {
			partnerError(w, err, log)
			return
		}
		w.Header().Set("Location", "/internal/v1/partners/"+strconv.Itoa(p.PartnerID))
		if replayed {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		status := http.StatusAccepted
		if p.SetupStatus == "complete" {
			status = http.StatusOK
		}
		writeJSON(w, status, p)
	}).Methods(http.MethodPost)
	r.HandleFunc("/internal/v1/partners/{partner_id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, mux.Vars(r)["partner_id"], "partner_id")
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		p, err := store.Get(ctx, id)
		if err != nil {
			partnerError(w, err, log)
			return
		}
		writeJSON(w, 200, p)
	}).Methods(http.MethodGet)
}

func partnerError(w http.ResponseWriter, err error, log *zap.Logger) {
	switch {
	case errors.Is(err, partners.ErrInstanceUnavailable):
		writeError(w, 409, "instance_unavailable", err.Error())
	case errors.Is(err, partners.ErrNotFound):
		writeError(w, 404, "not_found", "partner not found")
	case errors.Is(err, partners.ErrRequestConflict):
		writeError(w, 409, "idempotency_conflict", err.Error())
	case errors.Is(err, partners.ErrDomainConflict):
		writeError(w, 409, "domain_conflict", err.Error())
	case errors.Is(err, partners.ErrCapacity):
		writeError(w, 409, "id_capacity_exhausted", err.Error())
	default:
		log.Warn("partner database request failed")
		writeError(w, 503, "storage_unavailable", "partner storage is unavailable; retry with the same Idempotency-Key")
	}
}
