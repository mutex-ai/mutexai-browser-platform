package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
)

// InstanceStore is the operator registry's persistence boundary.
type InstanceStore interface {
	Create(context.Context, string, instances.CreateInput) (instances.Instance, bool, error)
	Get(context.Context, int) (instances.Instance, error)
	SetAvailability(context.Context, int, bool) (instances.Instance, error)
}

func instanceRoutes(r *mux.Router, store InstanceStore, log *zap.Logger) {
	r.HandleFunc("/internal/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		key, ok := creationKey(w, r)
		if !ok {
			return
		}
		var in instances.CreateInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.Normalize(); err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		instance, replayed, err := store.Create(ctx, key, in)
		if err != nil {
			instanceError(w, err, log)
			return
		}
		w.Header().Set("Location", "/internal/v1/instances/"+strconv.Itoa(instance.InstanceID))
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
			w.Header().Set("Idempotency-Replayed", "true")
		}
		writeJSON(w, status, instance)
	}).Methods(http.MethodPost)
	r.HandleFunc("/internal/v1/instances/{instance_id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, mux.Vars(r)["instance_id"], "instance_id")
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		instance, err := store.Get(ctx, id)
		if err != nil {
			instanceError(w, err, log)
			return
		}
		writeJSON(w, 200, instance)
	}).Methods(http.MethodGet)
	r.HandleFunc("/internal/v1/instances/{instance_id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, mux.Vars(r)["instance_id"], "instance_id")
		if !ok {
			return
		}
		var input struct {
			AcceptingClients *bool `json:"accepting_clients"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.AcceptingClients == nil {
			writeError(w, 400, "invalid_request", "accepting_clients is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		instance, err := store.SetAvailability(ctx, id, *input.AcceptingClients)
		if err != nil {
			instanceError(w, err, log)
			return
		}
		writeJSON(w, 200, instance)
	}).Methods(http.MethodPatch)
}

func instanceError(w http.ResponseWriter, err error, log *zap.Logger) {
	switch {
	case errors.Is(err, instances.ErrNotFound):
		writeError(w, 404, "not_found", err.Error())
	case errors.Is(err, instances.ErrRequestConflict):
		writeError(w, 409, "idempotency_conflict", err.Error())
	case errors.Is(err, instances.ErrConsoleConflict):
		writeError(w, 409, "console_conflict", err.Error())
	case errors.Is(err, instances.ErrCapacity):
		writeError(w, 409, "id_capacity_exhausted", err.Error())
	default:
		log.Warn("instance database request failed")
		writeError(w, 503, "storage_unavailable", "instance storage is unavailable; retry with the same Idempotency-Key")
	}
}
