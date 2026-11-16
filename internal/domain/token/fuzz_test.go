package token_test

import (
	"crypto"
	"crypto/elliptic"
	"errors"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/domain/token"
)

func FuzzVerify(f *testing.F) {
	rsaPrivate := rsaKey()
	ecPrivate := generateEC(f, elliptic.P256())

	validRS := forge(f, rsaPrivate, header(token.AlgRS256), validPayload())
	validES := forge(f, ecPrivate, header(token.AlgES256), validPayload())
	rsSegments := strings.Split(validRS, ".")

	seeds := []string{
		validRS,
		validES,
		"",
		".",
		"..",
		"...",
		"a.b.c",
		rsSegments[0] + "." + rsSegments[1] + ".",
		rsSegments[0] + ".." + rsSegments[2],
		"." + rsSegments[1] + "." + rsSegments[2],
		validRS + "=",
		validES + "\n",
		encode([]byte(header("none"))) + "." + rsSegments[1] + ".",
		encode([]byte(header("HS256"))) + "." + rsSegments[1] + "." + rsSegments[2],
		forge(f, rsaPrivate, `{"alg":"RS256","alg":"none","kid":"kid-1"}`, validPayload()),
		forge(f, rsaPrivate, `{"alg":"RS256","kid":"kid-1","crit":["exp"]}`, validPayload()),
		forge(f, rsaPrivate, header(token.AlgRS256), payload(`"1800000000"`, "1e999")),
		forge(f, rsaPrivate, header(token.AlgRS256), payload("1800000000.25", "-0.0")),
		forge(f, rsaPrivate, header(token.AlgRS256), `{"iss":null,"exp":[[[[]]]],"iat":{"a":{"a":1}}}`),
		forge(f, ecPrivate, header(token.AlgES256), `[`),
		encode([]byte(`{"alg":"ES256","kid":"\ud800"}`)) + "." + encode([]byte(`{`)) + "." + encode(make([]byte, 64)),
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	keys := func(_, alg string) (crypto.PublicKey, error) {
		if alg == token.AlgRS256 {
			return rsaPrivate.Public(), nil
		}

		return ecPrivate.Public(), nil
	}

	sentinels := []error{
		token.ErrMalformed, token.ErrUnsupportedAlgorithm, token.ErrSignature,
		token.ErrExpired, token.ErrNotYetValid, token.ErrClaimsInvalid,
	}

	f.Fuzz(func(t *testing.T, compact string) {
		claims, err := token.Verify(compact, keys, midLife)
		if err == nil {
			if claims.Issuer == "" || claims.Subject == "" || claims.SessionID == "" || claims.ID == "" {
				t.Fatalf("want complete claims from an accepted token, got %+v", claims)
			}

			if !midLife.Before(claims.ExpiresAt) {
				t.Fatalf("want an unexpired token, got exp %v", claims.ExpiresAt)
			}

			return
		}

		for _, sentinel := range sentinels {
			if errors.Is(err, sentinel) {
				return
			}
		}

		t.Fatalf("want one of the package's errors, got %v", err)
	})
}
