package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// APIError is the JSON body of every error response.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// WriteError writes an APIError with the given status.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, APIError{Code: code, Message: message})
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

// MaxBodyBytes is the largest request body DecodeJSON accepts.
const MaxBodyBytes = 1 << 20

// ErrUnsupportedMediaType is returned by DecodeJSON when the request is
// not application/json.
var ErrUnsupportedMediaType = errors.New("expected Content-Type: application/json")

// DecodeJSON decodes a request body holding exactly one JSON object into
// v. Unknown fields are rejected, so a typo or an unsupported field is an
// error instead of silently ignored data. Bodies over MaxBodyBytes fail
// with *http.MaxBytesError. w is needed so the server can close the
// connection when the limit is hit.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return ErrUnsupportedMediaType
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}

	// dec.More() misses trailing data such as `{} x`; a second Decode
	// that must hit EOF does not.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// WriteDecodeError writes the response for an error from DecodeJSON.
func WriteDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, ErrUnsupportedMediaType):
		WriteError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", err.Error())
	case errors.As(err, &tooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE",
			fmt.Sprintf("request body must not exceed %d bytes", tooLarge.Limit))
	default:
		WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	}
}
