package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/http/response"
)

const jwksMediaType = "application/jwk-set+json"

const jwksMaxAge = "public, max-age=300"

type jwkSet struct {
	Keys []key.JWK `json:"keys"`
}

func (h *Handler) JWKS(w http.ResponseWriter, r *http.Request) {
	slug := h.slug(r)

	found, err := h.realms.Resolve(r.Context(), slug)
	if err != nil {
		// Archived answers 404 like unknown: which realms exist is not this
		// endpoint's to disclose.
		if errors.Is(err, realm.ErrNotFound) || errors.Is(err, realm.ErrNotAvailable) {
			response.WriteError(w, http.StatusNotFound, response.Error{
				Code:        response.ErrorInvalidRequest,
				Description: "no realm named " + slug,
			})

			return
		}

		h.logger.Error().Err(err).Str("realm", slug).Msg("resolving a realm for the jwks")
		response.WriteServerError(w)

		return
	}

	published, err := h.keys.Published(r.Context(), found.ID())
	if err != nil {
		h.logger.Error().Err(err).Str("realm", slug).Msg("reading the realm keys")
		response.WriteServerError(w)

		return
	}

	// Non-nil, so a realm with nothing to publish serves {"keys":[]} and not
	// {"keys":null}.
	document := jwkSet{Keys: make([]key.JWK, 0, len(published))}

	for _, current := range published {
		jwk, jwkErr := current.JWK()
		if jwkErr != nil {
			h.logger.Error().Err(jwkErr).Str("realm", slug).Str("kid", current.KID()).
				Msg("projecting a key into a jwk")
			response.WriteServerError(w)

			return
		}

		document.Keys = append(document.Keys, jwk)
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		h.logger.Error().Err(err).Str("realm", slug).Msg("encoding the jwks")
		response.WriteServerError(w)

		return
	}

	// Over the rendered bytes: an unstable key order would re-ETag an
	// unchanged document.
	sum := sha256.Sum256(encoded)
	etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`

	w.Header().Set("Cache-Control", jwksMaxAge)
	w.Header().Set("ETag", etag)

	if matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)

		return
	}

	response.WriteBytes(w, http.StatusOK, jwksMediaType, encoded)
}

// matches is the If-None-Match comparison of RFC 9110 section 13.1.2: a comma
// separated list, `*` matching anything, and the weak prefix ignored because
// this comparison is the weak one.
func matches(header, etag string) bool {
	if header == "" {
		return false
	}

	if strings.TrimSpace(header) == "*" {
		return true
	}

	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
			return true
		}
	}

	return false
}
