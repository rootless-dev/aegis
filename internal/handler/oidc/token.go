package oidc

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/http/middleware"
	"github.com/rootless-dev/aegis/internal/http/response"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	grantPassword     = "password"
	grantRefreshToken = "refresh_token"
)

const formMediaType = "application/x-www-form-urlencoded"

const tokenMaxBody = 16 << 10

// One description per family: a caller must not learn whether the password
// was wrong or the identity disabled, nor why a refresh token was refused.
const (
	loginRefused        = "invalid user credentials"
	refreshTokenRefused = "the refresh token is not valid"
)

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	SessionState     string `json:"session_state"`
}

func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	// RFC 6749 section 5.1 requires both on every response carrying a token.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	found, ok := h.resolveRealm(w, r)
	if !ok {
		return
	}

	form, problem := readTokenForm(w, r)
	if problem != "" {
		writeTokenError(w, response.ErrorInvalidRequest, problem)

		return
	}

	client := service.ClientInfo{
		IP:        middleware.ClientIPFrom(r.Context()),
		UserAgent: r.UserAgent(),
	}

	var (
		tokens *service.Tokens
		err    error
	)

	switch form.Get("grant_type") {
	case "":
		writeTokenError(w, response.ErrorInvalidRequest, "grant_type is required")

		return
	case grantPassword:
		username, password := form.Get("username"), form.Get("password")
		if username == "" || password == "" {
			writeTokenError(w, response.ErrorInvalidRequest, "username and password are required")

			return
		}

		tokens, err = h.tokens.Login(r.Context(), found, username, password, client)
	case grantRefreshToken:
		refreshToken := form.Get("refresh_token")
		if refreshToken == "" {
			writeTokenError(w, response.ErrorInvalidRequest, "refresh_token is required")

			return
		}

		tokens, err = h.tokens.Refresh(r.Context(), found, refreshToken, client)

		// On this grant a disabled identity is one more way the token stopped
		// working, and answers like the others.
		if errors.Is(err, identity.ErrDisabled) {
			err = session.ErrEnded
		}
	default:
		writeTokenError(w, response.ErrorUnsupportedGrantType, "")

		return
	}

	if err != nil {
		h.refuse(w, found.Slug(), client.IP, err)

		return
	}

	if tokens.MustChangePassword {
		h.logger.Warn().Str("realm", found.Slug()).Str("session", tokens.SessionID.String()).
			Msg("tokens issued for a password marked for replacement")
	}

	response.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken:      tokens.AccessToken,
		TokenType:        "Bearer",
		ExpiresIn:        wholeSeconds(tokens.AccessTokenTTL),
		RefreshToken:     tokens.RefreshToken,
		RefreshExpiresIn: wholeSeconds(tokens.RefreshTokenTTL),
		SessionState:     tokens.SessionID.String(),
	})
}

// refuse logs no username: what is typed into a login field is often a
// password in the wrong box.
func (h *Handler) refuse(w http.ResponseWriter, slug, clientIP string, err error) {
	switch {
	case errors.Is(err, credential.ErrMismatch), errors.Is(err, identity.ErrDisabled):
		h.logger.Info().Str("realm", slug).Str("client_ip", clientIP).Msg("password grant refused")
		writeTokenError(w, response.ErrorInvalidGrant, loginRefused)

	case errors.Is(err, session.ErrReuseDetected):
		h.logger.Warn().Str("realm", slug).Str("client_ip", clientIP).
			Msg("refresh token reuse detected")
		writeTokenError(w, response.ErrorInvalidGrant, refreshTokenRefused)

	case errors.Is(err, session.ErrTokenNotFound), errors.Is(err, session.ErrTokenExpired),
		errors.Is(err, session.ErrEnded), errors.Is(err, session.ErrExpired):
		h.logger.Info().Str("realm", slug).Str("client_ip", clientIP).Msg("refresh token refused")
		writeTokenError(w, response.ErrorInvalidGrant, refreshTokenRefused)

	case errors.Is(err, service.ErrPasswordGrantDisabled):
		writeTokenError(w, response.ErrorUnauthorizedClient, "the password grant is not enabled for this realm")

	// invalid_grant: of the RFC 6749 section 5.2 codes it is the only one about
	// the grant not being honoured; the others blame the request or the client.
	case errors.Is(err, service.ErrRealmDisabled):
		writeTokenError(w, response.ErrorInvalidGrant, "the realm is disabled")

	default:
		h.logger.Error().Err(err).Str("realm", slug).Msg("issuing tokens")
		response.WriteServerError(w)
	}
}

// readTokenForm reads the parameters from the body alone, never the query
// string: a password in a URL ends up in access logs. A non-empty second
// result is what is wrong with the request.
func readTokenForm(w http.ResponseWriter, r *http.Request) (url.Values, string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != formMediaType {
		return nil, "the content type must be " + formMediaType
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tokenMaxBody))
	if err != nil {
		return nil, "the request body is too large or could not be read"
	}

	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, "the request body is not a valid form"
	}

	// RFC 6749 section 3.2: no parameter more than once, and one sent without
	// a value is treated as omitted.
	for name, values := range form {
		if len(values) > 1 {
			return nil, "a parameter is included more than once"
		}

		if values[0] == "" {
			delete(form, name)
		}
	}

	return form, ""
}

func writeTokenError(w http.ResponseWriter, code, description string) {
	response.WriteError(w, http.StatusBadRequest, response.Error{Code: code, Description: description})
}

func wholeSeconds(d time.Duration) int64 {
	return max(0, int64(d/time.Second))
}
