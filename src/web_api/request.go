package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

func creationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) {
		writeError(w, 400, "invalid_request", "Idempotency-Key must contain 1 to 128 visible ASCII characters")
		return "", false
	}
	return key, true
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		writeError(w, 415, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		jsonError(w, err)
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(w, 400, "invalid_request", "expected one JSON object")
		} else {
			jsonError(w, err)
		}
		return false
	}
	return true
}

func jsonError(w http.ResponseWriter, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		writeError(w, 413, "request_too_large", "request body exceeds 64 KiB")
	} else {
		writeError(w, 400, "invalid_request", "expected one JSON object with only the documented fields")
	}
}

func pathID(w http.ResponseWriter, raw, name string) (int, bool) {
	id, err := strconv.Atoi(raw)
	if err != nil || len(raw) != 4 || id < 1000 || id > 9999 {
		writeError(w, 400, "invalid_request", name+" must contain four digits from 1000 through 9999")
		return 0, false
	}
	return id, true
}
