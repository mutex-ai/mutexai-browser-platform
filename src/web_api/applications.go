package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/applications"
)

// ApplicationStore is the global registry's persistence boundary.
type ApplicationStore interface {
	Create(context.Context, string, applications.Definition) (applications.Record, bool, error)
	Update(context.Context, string, int64, applications.Definition) (applications.Record, error)
	Get(context.Context, string) (applications.Record, error)
	List(context.Context) ([]applications.Record, error)
}

type applicationHandler struct {
	store ApplicationStore
	log   *zap.Logger
}

func applicationRoutes(r *mux.Router, store ApplicationStore, log *zap.Logger) {
	h := applicationHandler{store: store, log: log}
	r.HandleFunc("/internal/v1/applications", h.list).Methods(http.MethodGet)
	r.HandleFunc("/internal/v1/applications", h.create).Methods(http.MethodPost)
	r.HandleFunc("/internal/v1/applications/{app_id}", h.get).Methods(http.MethodGet)
	r.HandleFunc("/internal/v1/applications/{app_id}", h.update).Methods(http.MethodPut)
}

func (h applicationHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := h.store.List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	if items == nil {
		items = []applications.Record{}
	}
	writeJSON(w, 200, struct {
		Items []applications.Record `json:"items"`
		Count int                   `json:"count"`
	}{items, len(items)})
}

func (h applicationHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	record, err := h.store.Get(ctx, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("ETag", applicationETag(record))
	writeJSON(w, 200, record)
}

func (h applicationHandler) create(w http.ResponseWriter, r *http.Request) {
	key, ok := creationKey(w, r)
	if !ok {
		return
	}
	in, ok := applicationInput(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	record, replayed, err := h.store.Create(ctx, key, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location", "/internal/v1/applications/"+record.ID)
	w.Header().Set("ETag", applicationETag(record))
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, status, record)
}

func (h applicationHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(w, r)
	if !ok {
		return
	}
	header := r.Header.Get("If-Match")
	if header == "" {
		writeError(w, 428, "precondition_required", "If-Match must contain the ETag returned by GET")
		return
	}
	value := strings.TrimPrefix(header, `"`+id+`:`)
	value = strings.TrimSuffix(value, `"`)
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 || header != fmt.Sprintf(`"%s:%d"`, id, revision) {
		writeError(w, 400, "invalid_request", "If-Match must contain one strong ETag returned by GET")
		return
	}
	in, ok := applicationInput(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	record, err := h.store.Update(ctx, id, revision, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("ETag", applicationETag(record))
	writeJSON(w, 200, record)
}

func applicationInput(w http.ResponseWriter, r *http.Request) (applications.Definition, bool) {
	var raw json.RawMessage
	if !readJSON(w, r, &raw) {
		return applications.Definition{}, false
	}
	in, err := applications.ParseDefinition(raw)
	if err != nil {
		if errors.Is(err, applications.ErrInvalid) {
			writeError(w, 400, "invalid_request", err.Error())
		} else {
			writeError(w, 500, "internal_error", "application validation unavailable")
		}
		return applications.Definition{}, false
	}
	return in, true
}

func applicationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := mux.Vars(r)["app_id"]
	if !applications.ValidID(id) {
		writeError(w, 400, "invalid_request", "app_id must contain 12 lowercase hexadecimal characters")
		return "", false
	}
	return id, true
}

func applicationETag(record applications.Record) string {
	return fmt.Sprintf(`"%s:%d"`, record.ID, record.Revision)
}

func (h applicationHandler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, applications.ErrNotFound):
		writeError(w, 404, "not_found", "application not found")
	case errors.Is(err, applications.ErrInvalid), errors.Is(err, applications.ErrDependency):
		writeError(w, 400, "invalid_request", err.Error())
	case errors.Is(err, applications.ErrRequestConflict):
		writeError(w, 409, "idempotency_conflict", err.Error())
	case errors.Is(err, applications.ErrSlugConflict):
		writeError(w, 409, "slug_conflict", err.Error())
	case errors.Is(err, applications.ErrHostConflict):
		writeError(w, 409, "host_conflict", err.Error())
	case errors.Is(err, applications.ErrRevisionConflict):
		writeError(w, 412, "revision_conflict", err.Error())
	default:
		h.log.Warn("application database request failed")
		writeError(w, 503, "storage_unavailable", "application storage is unavailable")
	}
}
