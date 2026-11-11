package token_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"errors"
	"math/big"
	"testing"

	"github.com/rootless-dev/aegis/internal/domain/token"
)

// From RFC 7515 appendices A.2 and A.3, pasted rather than computed here: with
// no second implementation to cross-check against, these are the proof that
// the wire format is the one other verifiers read.
const (
	rfc7515Payload = "eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFt" +
		"cGxlLmNvbS9pc19yb290Ijp0cnVlfQ"

	rfc7515RS256Header = "eyJhbGciOiJSUzI1NiJ9"
	rfc7515RS256N      = "ofgWCuLjybRlzo0tZWJjNiuSfb4p4fAkd_wWJcyQoTbji9k0l8W26mPddx" +
		"HmfHQp-Vaw-4qPCJrcS2mJPMEzP1Pt0Bm4d4QlL-yRT-SFd2lZS-pCgNMs" +
		"D1W_YpRPEwOWvG6b32690r2jZ47soMZo9wGzjb_7OMg0LOL-bSf63kpaSH" +
		"SXndS5z5rexMdbBYUsLA9e-KXBdQOS-UTo7WTBEMa2R2CapHg665xsmtdV" +
		"MTBQY4uDZlxvb3qCo5ZwKh9kG4LT6_I5IhlJH7aGhyxXFvUK-DWNmoudF8" +
		"NAco9_h9iaGNj8q2ethFkMLs91kzk2PAcDTW9gb54h4FRWyuXpoQ"
	rfc7515RS256Signature = "cC4hiUPoj9Eetdgtv3hF80EGrhuB__dzERat0XF9g2VtQgr9PJbu3XOiZj5RZmh7" +
		"AAuHIm4Bh-0Qc_lF5YKt_O8W2Fp5jujGbds9uJdbF9CUAr7t1dnZcAcQjbKBYNX4" +
		"BAynRFdiuB--f_nZLgrnbyTyWzO75vRK5h6xBArLIARNPvkSjtQBMHlb1L07Qe7K" +
		"0GarZRmB_eSN9383LcOLn6_dO--xi12jzDwusC-eOkHWEsqtFZESc6BfI7noOPqv" +
		"hJ1phCnvWh6IeYI2w9QOYEUipUTI8np6LbgGY9Fs98rqVt5AXLIhWkWywlVmtVrB" +
		"p0igcN_IoypGlUPQGe77Rw"

	rfc7515ES256Header    = "eyJhbGciOiJFUzI1NiJ9"
	rfc7515ES256X         = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"
	rfc7515ES256Y         = "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"
	rfc7515ES256Signature = "DtEhU3ljbEg8L38VWAfUAqOyKAM6-Xx-F4GawxaepmXFCgfTjDxw5djxLa8ISlSA" +
		"pmWQxfKTUJqPP3-Kg6NU1Q"
)

func TestVerifySignatureAcceptsTheRFCVectors(t *testing.T) {
	number := func(encoded string) *big.Int {
		return new(big.Int).SetBytes(decode(t, encoded))
	}

	cases := []struct {
		alg       string
		public    crypto.PublicKey
		header    string
		signature string
	}{
		{
			alg:       token.AlgRS256,
			public:    &rsa.PublicKey{N: number(rfc7515RS256N), E: 65537},
			header:    rfc7515RS256Header,
			signature: rfc7515RS256Signature,
		},
		{
			alg: token.AlgES256,
			public: &ecdsa.PublicKey{
				Curve: elliptic.P256(),
				X:     number(rfc7515ES256X),
				Y:     number(rfc7515ES256Y),
			},
			header:    rfc7515ES256Header,
			signature: rfc7515ES256Signature,
		},
	}

	for _, tc := range cases {
		t.Run(tc.alg, func(t *testing.T) {
			signingInput := tc.header + "." + rfc7515Payload
			signature := decode(t, tc.signature)

			if err := token.VerifySignature(tc.alg, tc.public, signingInput, signature); err != nil {
				t.Errorf("want the vector to verify, got %v", err)
			}

			err := token.VerifySignature(tc.alg, tc.public, signingInput+"A", signature)
			if !errors.Is(err, token.ErrSignature) {
				t.Errorf("want %v for another signing input, got %v", token.ErrSignature, err)
			}
		})
	}
}
