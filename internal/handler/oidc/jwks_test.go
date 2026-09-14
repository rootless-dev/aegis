package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/handler/oidc"
)

func TestJWKSServesThePublishedKeys(t *testing.T) {
	handler, keys := newHandler(t, realm.StatusActive)

	recorder := serve(t, handler, http.MethodGet, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("want the registered media type, got %q", got)
	}

	var document struct {
		Keys []map[string]any `json:"keys"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(document.Keys) != len(keys) {
		t.Fatalf("want %d keys, got %d", len(keys), len(document.Keys))
	}

	if document.Keys[0]["kid"] != keys[0].KID() {
		t.Errorf("want kid %q, got %v", keys[0].KID(), document.Keys[0]["kid"])
	}
}

func TestJWKSPublishesTheKeyThatSigns(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	built, err := key.New(
		uuid.New(), key.PurposeSignature, key.AlgorithmRS256,
		&private.PublicKey, []byte("sealed"), "kek",
	)
	if err != nil {
		t.Fatalf("building a key: %v", err)
	}

	handler := oidc.New(
		fakeResolver{status: realm.StatusActive},
		fakeKeys{keys: []*key.Key{built}},
		func(*http.Request) string { return "acme" },
		testLogger(),
	)

	recorder := serve(t, handler, http.MethodGet, nil)

	var document struct {
		Keys []struct {
			N string `json:"n"`
			E string `json:"e"`
		} `json:"keys"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	modulus, err := base64.RawURLEncoding.DecodeString(document.Keys[0].N)
	if err != nil {
		t.Fatalf("decoding n: %v", err)
	}

	exponent, err := base64.RawURLEncoding.DecodeString(document.Keys[0].E)
	if err != nil {
		t.Fatalf("decoding e: %v", err)
	}

	rebuilt := &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(new(big.Int).SetBytes(exponent).Int64()),
	}

	if !rebuilt.Equal(&private.PublicKey) {
		t.Error("want the published key to be the signing key, got a different one")
	}
}

func TestJWKSCachesAndAnswersConditionally(t *testing.T) {
	handler, _ := newHandler(t, realm.StatusActive)

	first := serve(t, handler, http.MethodGet, nil)

	if got := first.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("want the cache header, got %q", got)
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("want an ETag, got none")
	}

	second := serve(t, handler, http.MethodGet, http.Header{"If-None-Match": []string{etag}})

	if second.Code != http.StatusNotModified {
		t.Errorf("want 304, got %d", second.Code)
	}

	if second.Body.Len() != 0 {
		t.Errorf("want no body on a 304, got %q", second.Body.String())
	}
}

func TestJWKSServesADisabledRealm(t *testing.T) {
	handler, _ := newHandler(t, realm.StatusDisabled)

	if recorder := serve(t, handler, http.MethodGet, nil); recorder.Code != http.StatusOK {
		t.Errorf("want 200, got %d", recorder.Code)
	}
}

func TestJWKSRefusesAnArchivedRealm(t *testing.T) {
	handler := oidc.New(
		fakeResolver{err: realm.ErrNotAvailable},
		fakeKeys{},
		func(*http.Request) string { return "acme" },
		testLogger(),
	)

	recorder := serve(t, handler, http.MethodGet, nil)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json;charset=UTF-8" {
		t.Errorf("want a json error, got %q", got)
	}
}

func TestJWKSRefusesAnUnknownRealm(t *testing.T) {
	handler := oidc.New(
		fakeResolver{err: realm.ErrNotFound},
		fakeKeys{},
		func(*http.Request) string { return "nope" },
		testLogger(),
	)

	if recorder := serve(t, handler, http.MethodGet, nil); recorder.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", recorder.Code)
	}
}

func TestJWKSServesAnEmptySetRatherThan404(t *testing.T) {
	handler := oidc.New(
		fakeResolver{status: realm.StatusActive},
		fakeKeys{},
		func(*http.Request) string { return "acme" },
		testLogger(),
	)

	recorder := serve(t, handler, http.MethodGet, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}

	if got := recorder.Body.String(); got != `{"keys":[]}` {
		t.Errorf("want an empty set, got %q", got)
	}
}

// --- fakes and helpers ---

type fakeResolver struct {
	status realm.Status
	err    error
}

func (f fakeResolver) Resolve(_ context.Context, slug string) (*realm.Realm, error) {
	if f.err != nil {
		return nil, f.err
	}

	base, err := url.Parse("https://auth.example.com")
	if err != nil {
		return nil, err
	}

	built, err := realm.New(slug, slug, base)
	if err != nil {
		return nil, err
	}

	if f.status != realm.StatusActive {
		if err := built.SetStatus(f.status); err != nil {
			return nil, err
		}
	}

	return built, nil
}

type fakeKeys struct{ keys []*key.Key }

func (f fakeKeys) Published(_ context.Context, _ uuid.UUID) ([]*key.Key, error) {
	return f.keys, nil
}

func newHandler(t *testing.T, status realm.Status) (*oidc.Handler, []*key.Key) {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	built, err := key.New(
		uuid.New(), key.PurposeSignature, key.AlgorithmES256,
		&private.PublicKey, []byte("sealed"), "kek",
	)
	if err != nil {
		t.Fatalf("building a key: %v", err)
	}

	keys := []*key.Key{built}

	return oidc.New(
		fakeResolver{status: status},
		fakeKeys{keys: keys},
		func(*http.Request) string { return "acme" },
		testLogger(),
	), keys
}

func serve(t *testing.T, handler *oidc.Handler, method string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, "/realms/acme"+oidc.JWKSPath, nil)
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}

	recorder := httptest.NewRecorder()
	handler.JWKS(recorder, request)

	return recorder
}

func testLogger() *log.Logger {
	return &log.Logger{Writer: log.IOWriter{Writer: io.Discard}}
}
