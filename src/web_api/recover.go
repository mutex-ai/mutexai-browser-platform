package api

import (
	"errors"
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"
)

// recoverPanics logs unexpected panics and lets net/http abort the response.
// Appending a JSON error could turn a partial response into a misleading success.
func recoverPanics(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					err, _ := rec.(error)
					if !errors.Is(err, http.ErrAbortHandler) {
						// Panic values and request details may contain credentials.
						log.Error("handler panic", zap.ByteString("stack", debug.Stack()))
					}
					panic(http.ErrAbortHandler)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
