//go:build integration

// The realm keys end to end. Always Postgres, like the rest of this package:
// these cases are about the binary, not engine coverage, which
// internal/repository covers across all four.
package integration_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const jwksPath = "/realms/master/protocol/openid-connect/certs"

type jwkSet struct {
	Keys []struct {
		Kty string `json:"kty"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	} `json:"keys"`
}

// The issuer is derived from AEGIS_PUBLIC_URL once, at seed time, and a boot whose
// public url moved is refused in production: a test that restarts has to come
// back on the same port.
func keyServer(t *testing.T, port string, env []string) *instance {
	t.Helper()

	server := launch(t, port, "http", http.DefaultClient, env, nil)
	waitUntilReady(t, server)

	return server
}

func TestTheMasterRealmIsSeededWithTwoKeys(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	status, body := server.get(t, jwksPath)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", status, body)
	}

	set := decodeJWKS(t, body)

	if len(set.Keys) != 2 {
		t.Fatalf("want 2 keys, got %d: %s", len(set.Keys), body)
	}

	algorithms := map[string]bool{}

	for _, published := range set.Keys {
		algorithms[published.Alg] = true

		if published.Use != "sig" {
			t.Errorf("want use sig, got %q", published.Use)
		}

		if published.Kid == "" {
			t.Error("want a kid, got none")
		}
	}

	if !algorithms["RS256"] || !algorithms["ES256"] {
		t.Errorf("want RS256 and ES256, got %v", algorithms)
	}
}

func TestBootingASecondTimeKeepsTheSameKeys(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	first := keyServer(t, port, env)
	_, body := first.get(t, jwksPath)
	before := kidsOf(t, body)
	stopBinary(t, first)

	second := keyServer(t, port, env)
	defer stopBinary(t, second)

	_, body = second.get(t, jwksPath)

	if got := kidsOf(t, body); !equalSets(before, got) {
		t.Errorf("want the same keys after a restart, got %v then %v", before, got)
	}
}

func TestTheJWKSIsCacheable(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	response, err := http.Get(server.url(jwksPath))
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer response.Body.Close()

	if got := response.Header.Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("want the cache header, got %q", got)
	}

	if got := response.Header.Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("want the registered media type, got %q", got)
	}

	etag := response.Header.Get("ETag")
	if etag == "" {
		t.Fatal("want an ETag, got none")
	}

	request, err := http.NewRequest(http.MethodGet, server.url(jwksPath), nil)
	if err != nil {
		t.Fatalf("building the conditional request: %v", err)
	}

	request.Header.Set("If-None-Match", etag)

	conditional, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer conditional.Body.Close()

	if conditional.StatusCode != http.StatusNotModified {
		t.Errorf("want 304, got %d", conditional.StatusCode)
	}
}

func TestRotateAddsAKeyAndKeepsTheOldOnePublished(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	_, body := server.get(t, jwksPath)
	before := kidsOf(t, body)

	if code, output := runCommand(t, env, "key", "rotate", "master", "RS256"); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, output)
	}

	_, body = server.get(t, jwksPath)
	after := kidsOf(t, body)

	if len(after) != 3 {
		t.Fatalf("want 3 published keys, got %d: %s", len(after), body)
	}

	// Every key that was published stays published: a token signed a second
	// before the rotation has to remain verifiable.
	for _, kid := range before {
		if !slices.Contains(after, kid) {
			t.Errorf("want %q still published, got %v", kid, after)
		}
	}
}

func TestDisableRefusesAnActiveKeyAndLeavesTheJWKSAlone(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	_, body := server.get(t, jwksPath)
	before := kidsOf(t, body)

	code, output := runCommand(t, env, "key", "disable", "master", before[0])
	if code != 1 {
		t.Fatalf("want exit 1, got %d: %s", code, output)
	}

	if !strings.Contains(output, "rotate") {
		t.Errorf("want the message to name the way out, got %q", output)
	}

	_, body = server.get(t, jwksPath)

	if got := kidsOf(t, body); !equalSets(before, got) {
		t.Errorf("want the jwks unchanged, got %v then %v", before, got)
	}
}

func TestDisableRemovesAPassiveKey(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	_, body := server.get(t, jwksPath)
	outgoing := rsaKID(t, body)

	if code, output := runCommand(t, env, "key", "rotate", "master", "RS256"); code != 0 {
		t.Fatalf("rotating: %d %s", code, output)
	}

	if code, output := runCommand(t, env, "key", "disable", "master", outgoing); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, output)
	}

	_, body = server.get(t, jwksPath)

	if slices.Contains(kidsOf(t, body), outgoing) {
		t.Errorf("want %q out of the jwks, got it still published", outgoing)
	}
}

// The point of the recorded kek id: an interrupted rewrap leaves a consistent
// database, and running the command again finishes the job.
func TestRewrapMovesEveryKeyToTheNewMasterKey(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	_, body := server.get(t, jwksPath)
	before := kidsOf(t, body)
	stopBinary(t, server)

	const nextKey = "cHJldmlvdXMtbWFzdGVyLWtleS1mb3ItdGhlLXRlc3Q="

	rewrapEnv := replaceEnv(env, "AEGIS_CRYPTO_MASTER_KEY", nextKey)
	rewrapEnv = append(rewrapEnv, "AEGIS_CRYPTO_MASTER_KEY_PREVIOUS="+valueOf(env, "AEGIS_CRYPTO_MASTER_KEY"))

	if code, output := runCommand(t, rewrapEnv, "key", "rewrap"); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, output)
	}

	code, output := runCommand(t, rewrapEnv, "key", "rewrap")
	if code != 0 || !strings.Contains(output, "0 keys") {
		t.Errorf("want a second run to move nothing, got %d: %s", code, output)
	}

	restarted := keyServer(t, port, replaceEnv(env, "AEGIS_CRYPTO_MASTER_KEY", nextKey))
	defer stopBinary(t, restarted)

	_, body = restarted.get(t, jwksPath)

	if got := kidsOf(t, body); !equalSets(before, got) {
		t.Errorf("want the same keys after a rewrap, got %v then %v", before, got)
	}
}

func TestProductionWithoutAMasterKeyDoesNotBoot(t *testing.T) {
	env, _ := schemaEnv(t, freePort(t))

	output, err := runBinaryToCompletion(t, nil, removeEnv(env, "AEGIS_CRYPTO_MASTER_KEY"))
	if err == nil {
		t.Fatal("want the boot to fail, got success")
	}

	if !strings.Contains(output, "openssl rand -base64 32") {
		t.Errorf("want the message to say how to generate a key, got %q", output)
	}
}

func TestAWrongPathUnderRealmsAnswersJSON(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	status, body := server.get(t, "/realms/master/protocol/openid-connect/nope")

	if status != http.StatusNotFound {
		t.Fatalf("want 404, got %d", status)
	}

	// An HTML page here would mean the branch was registered inline instead of
	// mounted.
	if !strings.Contains(body, `"error"`) {
		t.Errorf("want a json error body, got %q", body)
	}
}

func TestAnUnknownRealmAnswers404(t *testing.T) {
	port := freePort(t)
	env, _ := schemaEnv(t, port)

	server := keyServer(t, port, env)
	defer stopBinary(t, server)

	if status, _ := server.get(t, "/realms/nope/protocol/openid-connect/certs"); status != http.StatusNotFound {
		t.Errorf("want 404, got %d", status)
	}
}

func decodeJWKS(t *testing.T, body string) jwkSet {
	t.Helper()

	var set jwkSet
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("decoding the jwks: %v: %s", err, body)
	}

	return set
}

func kidsOf(t *testing.T, body string) []string {
	t.Helper()

	set := decodeJWKS(t, body)
	kids := make([]string, 0, len(set.Keys))

	for _, published := range set.Keys {
		kids = append(kids, published.Kid)
	}

	return kids
}

func rsaKID(t *testing.T, body string) string {
	t.Helper()

	for _, published := range decodeJWKS(t, body).Keys {
		if published.Alg == "RS256" {
			return published.Kid
		}
	}

	t.Fatal("no RS256 key in the jwks")

	return ""
}

// Order is the endpoint's business: asserting on it here would fail a change
// that broke nothing.
func equalSets(first, second []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(first)), slices.Sorted(slices.Values(second)))
}
