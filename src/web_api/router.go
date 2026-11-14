// Package api exposes the private Platform HTTP endpoints.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

// Pinger is implemented by the PostgreSQL connection pool.
type Pinger interface {
	PingContext(context.Context) error
}

// NewRouter mounts only the implemented private endpoints.
func NewRouter(db Pinger, log *zap.Logger, store PartnerStore, instances InstanceStore, applications ApplicationStore) http.Handler {
	r := mux.NewRouter()
	r.Use(recoverPanics(log))
	r.HandleFunc("/internal/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}).Methods(http.MethodGet)
	r.HandleFunc("/internal/v1/health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Warn("PostgreSQL readiness check failed")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "postgresql": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "postgresql": "ok"})
	}).Methods(http.MethodGet)
	if store != nil {
		partnerRoutes(r, store, log)
	}
	if instances != nil {
		instanceRoutes(r, instances, log)
	}
	if applications != nil {
		applicationRoutes(r, applications, log)
	}
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}
