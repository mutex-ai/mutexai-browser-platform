package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONEncodingFailure(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, make(chan int))
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"internal_error\",\"message\":\"internal error\"}\n" {
		t.Fatalf("encoding failure returned %d %s", w.Code, w.Body.String())
	}
}
