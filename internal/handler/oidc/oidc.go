// Package oidc serves the protocol endpoints of a realm. It answers JSON, in
// the OAuth 2 error format the rest of the service writes, never HTML.
package oidc

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

const JWKSPath = "/protocol/openid-connect/certs"

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

// SlugFunc reads the realm slug the router matched, injected so this package
// need not import the router.
type SlugFunc func(r *http.Request) string

type Router interface {
	Get(pattern string, h http.HandlerFunc)
	Head(pattern string, h http.HandlerFunc)
}

type Handler struct {
	realms RealmResolver
	keys   KeyPublisher
	slug   SlugFunc
	logger *log.Logger
}

func New(realms RealmResolver, keys KeyPublisher, slug SlugFunc, logger *log.Logger) *Handler {
	return &Handler{realms: realms, keys: keys, slug: slug, logger: logger}
}

// Mount pairs a HEAD with every GET: the router matches by method, so the
// alternative is a 405 for a method the endpoint supports.
func (h *Handler) Mount(router Router) {
	router.Get(realmPattern+JWKSPath, h.JWKS)
	router.Head(realmPattern+JWKSPath, h.JWKS)
}
