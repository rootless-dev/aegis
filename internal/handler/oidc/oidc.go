// Package oidc serves the protocol endpoints of a realm. It answers JSON, in
// the OAuth 2 error format the rest of the service writes, never HTML.
package oidc

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/http/response"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	JWKSPath  = "/protocol/openid-connect/certs"
	TokenPath = "/protocol/openid-connect/token"
)

// RealmParam is the name the router binds the slug to, and the name the
// assembly reads back through SlugFunc. Exported because a rename here would
// otherwise 404 every request with nothing failing to compile.
const RealmParam = "realm"

const realmPattern = "/{" + RealmParam + "}"

type RealmResolver interface {
	Resolve(ctx context.Context, slug string) (*realm.Realm, error)
}

// KeyPublisher returns what belongs in the JWKS: active and passive keys, never
// disabled ones.
type KeyPublisher interface {
	Published(ctx context.Context, realmID uuid.UUID) ([]*key.Key, error)
}

type TokenIssuer interface {
	Login(ctx context.Context, r *realm.Realm, login, password string, client service.ClientInfo) (*service.Tokens, error)
	Refresh(ctx context.Context, r *realm.Realm, refreshToken string, client service.ClientInfo) (*service.Tokens, error)
}

// SlugFunc reads the realm slug the router matched, injected so this package
// need not import the router.
type SlugFunc func(r *http.Request) string

type Router interface {
	Get(pattern string, h http.HandlerFunc)
	Head(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

type Handler struct {
	realms RealmResolver
	keys   KeyPublisher
	tokens TokenIssuer
	slug   SlugFunc
	logger *log.Logger
}

func New(
	realms RealmResolver, keys KeyPublisher, tokens TokenIssuer, slug SlugFunc, logger *log.Logger,
) *Handler {
	return &Handler{realms: realms, keys: keys, tokens: tokens, slug: slug, logger: logger}
}

// Mount pairs a HEAD with every GET: the router matches by method, so the
// alternative is a 405 for a method the endpoint supports.
func (h *Handler) Mount(router Router) {
	router.Get(realmPattern+JWKSPath, h.JWKS)
	router.Head(realmPattern+JWKSPath, h.JWKS)
	router.Post(realmPattern+TokenPath, h.Token)
}

// resolveRealm writes the response itself when it reports false.
func (h *Handler) resolveRealm(w http.ResponseWriter, r *http.Request) (*realm.Realm, bool) {
	slug := h.slug(r)

	found, err := h.realms.Resolve(r.Context(), slug)
	if err == nil {
		return found, true
	}

	// Archived answers 404 like unknown: which realms exist is not this
	// surface's to disclose.
	if errors.Is(err, realm.ErrNotFound) || errors.Is(err, realm.ErrNotAvailable) {
		response.WriteError(w, http.StatusNotFound, response.Error{
			Code:        response.ErrorInvalidRequest,
			Description: "no realm named " + slug,
		})

		return nil, false
	}

	h.logger.Error().Err(err).Str("realm", slug).Msg("resolving a realm")
	response.WriteServerError(w)

	return nil, false
}
