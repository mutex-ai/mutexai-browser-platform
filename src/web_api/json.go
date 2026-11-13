package api

import (
	"encoding/json"
	"net/http"
)

// responseError is the standard JSON error body.
type responseError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":"internal_error","message":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A failed write cannot be repaired after the response has started.
	_, _ = w.Write(append(data, '\n'))
}

// writeError writes a stable error code and a safe message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, responseError{Error: code, Message: message})
}
