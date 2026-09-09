package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// errorBody is what every failure looks like on the wire: one Russian sentence,
// never a Go error string.
type errorBody struct {
	Message string `json:"message"`
}

// writeJSON sends one value as JSON.
//
// The encode error is deliberately dropped: by the time it could happen the
// status line and part of the body are already on the wire, so there is nothing
// left to tell the client. Every value this package encodes is a plain struct
// of strings, numbers and slices, none of which json can refuse.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a failure with a Russian message.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Message: message})
}

// decodeBody reads a JSON request body into dst. An empty body is not an error:
// several routes take no fields at all, and a UI that sends nothing for them is
// behaving correctly.
//
// It reports whether decoding succeeded, having already answered the client
// when it did not.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return true
	case isTooLarge(err):
		writeError(w, http.StatusRequestEntityTooLarge, msgBodyTooBig)
		return false
	default:
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return false
	}
}

// isTooLarge reports whether the body ran past maxBodyBytes.
func isTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}
