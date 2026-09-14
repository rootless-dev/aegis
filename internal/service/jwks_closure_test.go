package service_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/handler/oidc"
	"github.com/rootless-dev/aegis/internal/infra/keygen"
	"github.com/rootless-dev/aegis/internal/infra/sealer"
	"github.com/rootless-dev/aegis/internal/service"
)

// This is the assertion the whole slice exists to make true: a signature made
// with the key the Signer port hands out verifies under the key the JWKS
// endpoint published, found by the kid the port reported.
//
// It runs against the real sealer, the real generator and the real handler,
// because every fake in between is a place the two halves could agree with each
// other while disagreeing with a client.
func TestASignatureVerifiesUnderTheKeyTheJWKSPublished(t *testing.T) {
	for _, algorithm := range []key.Algorithm{key.AlgorithmRS256, key.AlgorithmES256} {
		t.Run(algorithm.String(), func(t *testing.T) {
			keys, provisioned := provisionRealm(t, algorithm)

			served := serveJWKS(t, keys, provisioned)

			signer, ref, err := keys.SignerFor(context.Background(), provisioned.ID(), algorithm)
			if err != nil {
				t.Fatalf("asking for a signer: %v", err)
			}

			digest := sha256.Sum256([]byte("the payload a token would carry"))

			signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
			if err != nil {
				t.Fatalf("signing: %v", err)
			}

			published := publishedKey(t, served, ref.KID)

			verify(t, algorithm, published, digest[:], signature)
		})
	}
}

func provisionRealm(t *testing.T, algorithm key.Algorithm) (*service.KeyService, *realm.Realm) {
	t.Helper()

	base, err := url.Parse("https://auth.example.com")
	if err != nil {
		t.Fatalf("parsing the base url: %v", err)
	}

	created, err := realm.New("acme", "Acme", base)
	if err != nil {
		t.Fatalf("building a realm: %v", err)
	}

	keeper, err := sealer.New([]byte("0123456789abcdef0123456789abcdef"), nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	store := &fakeStore{repo: newFakeRepo(), keys: newFakeKeys()}
	keys := service.NewKeyService(store, keeper, keygen.New())

	if _, err := keys.EnsureKeys(context.Background(), created.ID(), algorithm); err != nil {
		t.Fatalf("provisioning: %v", err)
	}

	return keys, created
}

// serveJWKS goes through the real handler, so what the assertions read is the
// response body a client would receive rather than the projection behind it.
func serveJWKS(t *testing.T, keys *service.KeyService, of *realm.Realm) []byte {
	t.Helper()

	handler := oidc.New(
		staticResolver{realm: of},
		keys,
		func(*http.Request) string { return of.Slug() },
		&log.Logger{Writer: log.IOWriter{Writer: io.Discard}},
	)

	recorder := httptest.NewRecorder()
	handler.JWKS(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200 from the jwks, got %d: %s", recorder.Code, recorder.Body)
	}

	return recorder.Body.Bytes()
}

type staticResolver struct{ realm *realm.Realm }

func (s staticResolver) Resolve(context.Context, string) (*realm.Realm, error) {
	return s.realm, nil
}

// publishedKey rebuilds a public key from the served members, the way a client
// library does. Rebuilding rather than comparing is what makes the encoding
// itself part of the assertion: a coordinate padded on the wrong side, or a
// modulus carrying a leading zero, produces a different number here and fails
// the verification below.
func publishedKey(t *testing.T, served []byte, kid string) crypto.PublicKey {
	t.Helper()

	var document struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}

	if err := json.Unmarshal(served, &document); err != nil {
		t.Fatalf("decoding the jwks: %v", err)
	}

	for _, candidate := range document.Keys {
		if candidate.Kid != kid {
			continue
		}

		if candidate.N != "" {
			return &rsa.PublicKey{
				N: unsigned(t, candidate.N),
				E: int(unsigned(t, candidate.E).Int64()),
			}
		}

		if candidate.Crv != "P-256" {
			t.Fatalf("want curve P-256, got %q", candidate.Crv)
		}

		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     unsigned(t, candidate.X),
			Y:     unsigned(t, candidate.Y),
		}
	}

	t.Fatalf("no key with kid %q in the served jwks: %s", kid, served)

	return nil
}

func verify(t *testing.T, algorithm key.Algorithm, public crypto.PublicKey, digest, signature []byte) {
	t.Helper()

	switch algorithm {
	case key.AlgorithmRS256:
		rsaKey, ok := public.(*rsa.PublicKey)
		if !ok {
			t.Fatalf("want an rsa key rebuilt from the jwks, got %T", public)
		}

		if err := rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest, signature); err != nil {
			t.Fatalf("the published key does not verify what the active key signed: %v", err)
		}
	case key.AlgorithmES256:
		ecKey, ok := public.(*ecdsa.PublicKey)
		if !ok {
			t.Fatalf("want an ec key rebuilt from the jwks, got %T", public)
		}

		// ASN.1, because that is what ecdsa.PrivateKey.Sign produces. A JWS
		// carries the fixed-width R ‖ S instead, and converting between them is
		// the caller's job — the trap this port's shape leaves for slice 4.
		if !ecdsa.VerifyASN1(ecKey, digest, signature) {
			t.Fatal("the published key does not verify what the active key signed")
		}
	}
}

func unsigned(t *testing.T, encoded string) *big.Int {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decoding a jwk member: %v", err)
	}

	return new(big.Int).SetBytes(raw)
}
