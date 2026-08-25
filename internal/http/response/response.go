// Package response writes the HTTP payloads shared by every endpoint.
package response

import (
	"encoding/json"
	"net/http"
)

// OAuth 2 error codes, RFC 6749 section 5.2.
const (
	ErrorInvalidRequest         = "invalid_request"
	ErrorInvalidClient          = "invalid_client"
	ErrorInvalidGrant           = "invalid_grant"
	ErrorUnauthorizedClient     = "unauthorized_client"
	ErrorUnsupportedGrantType   = "unsupported_grant_type"
	ErrorInvalidScope           = "invalid_scope"
	ErrorServerError            = "server_error"
	ErrorTemporarilyUnavailable = "temporarily_unavailable"
)

type Error struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	URI         string `json:"error_uri,omitempty"`
}

// WriteError answers in the OAuth 2 error format, which the spec requires to
// carry no-store.
func WriteError(w http.ResponseWriter, status int, err Error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	WriteJSON(w, status, err)
}

func WriteServerError(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, Error{Code: ErrorServerError})
}

// ServerError is WriteServerError in the shape the recoverer takes; the request
// is in the signature because the HTML writer needs it.
func ServerError(w http.ResponseWriter, _ *http.Request) {
	WriteServerError(w)
}

// WriteJSON sets no cache headers of its own: discovery and JWKS are meant to
// be cached, so endpoints that must not be set no-store themselves.
//
// The payload is serialized before the status is written, so a failure to
// encode answers 500 instead of a truncated body under a success status.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		// Written inline rather than through WriteServerError, which would come
		// back here and could recurse.
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"` + ErrorServerError + `"}`))

		return
	}

	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)

	_, _ = w.Write(encoded)
}
