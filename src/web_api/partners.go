package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/mutex-ai/mutexai-browser-platform/src/partners"
	"go.uber.org/zap"
)

// PartnerStore is the persistence boundary for the operator API.
type PartnerStore interface {
	Create(context.Context, string, partners.CreateInput) (partners.Partner, bool, error)
	Get(context.Context, int) (partners.Partner, error)
}

func partnerRoutes(r *mux.Router, store PartnerStore, log *zap.Logger) {
	r.HandleFunc("/internal/v1/partners", func(w http.ResponseWriter, r *http.Request) {
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/json" {
			writeError(w, 415, "unsupported_media_type", "Content-Type must be application/json")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) {
			writeError(w, 400, "invalid_request", "Idempotency-Key must contain 1 to 128 visible ASCII characters")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var in partners.CreateInput
		if err := decoder.Decode(&in); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, 413, "request_too_large", "request body exceeds 64 KiB")
				return
			}
			writeError(w, 400, "invalid_request", "expected one JSON object with name, client_name, site_name and console_domains")
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			writeError(w, 400, "invalid_request", "expected one JSON object")
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
		raw := mux.Vars(r)["partner_id"]
		id, err := strconv.Atoi(raw)
		if err != nil || len(raw) != 4 || id < 1000 || id > 9999 {
			writeError(w, 400, "invalid_request", "partner_id must contain four digits from 1000 through 9999")
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
