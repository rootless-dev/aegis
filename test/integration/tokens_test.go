//go:build integration

// The token endpoint and `aegisd session` end to end. Always Postgres, like the
// rest of this package.
package integration_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/token"
)

const (
	tokenPath  = "/realms/master/protocol/openid-connect/token"
	tokenEmail = "ana@example.com"
	tokenLogin = "ana"
)

var (
	createdIdentity = regexp.MustCompile(`created identity (\S+) in realm master`)
	sha256Hex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	SessionState     string `json:"session_state"`

	Error string `json:"error"`
}

// tokenFixture is a running server with one identity in the master realm. It
// remembers every secret it saw and every output it produced, so that no test
// has to remember to check one against the other.
type tokenFixture struct {
	server     *instance
	env        []string
	endpoint   schemaEndpoint
	issuer     string
	identityID string

	secrets []string
	outputs []string
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()

	port := freePort(t)
	env, endpoint := schemaEnv(t, port)

	f := &tokenFixture{
		server:   keyServer(t, port, env),
		env:      env,
		endpoint: endpoint,
		issuer:   "http://127.0.0.1:" + port + "/realms/master",
		secrets:  []string{identityPassword},
	}

	// Registered after launch's Kill, so it runs before it: the log is only
	// complete, and only safe to read, once the process has exited.
	t.Cleanup(func() {
		stopBinary(t, f.server)
		f.assertNothingLeaked(t, "the server log", f.server.output.String())

		for _, output := range f.outputs {
			f.assertNothingLeaked(t, "a command's output", output)
		}
	})

	_, output := f.command(t, withPasswordEnv(env, identityPassword), "identity", "create", "master", tokenEmail, tokenLogin)

	match := createdIdentity.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("want the created identity's id in the output, got:\n%s", output)
	}

	f.identityID = match[1]

	return f
}

func (f *tokenFixture) assertNothingLeaked(t *testing.T, where, content string) {
	t.Helper()

	for _, secret := range f.secrets {
		if strings.Contains(content, secret) {
			t.Errorf("want no password or token in %s, got %q in:\n%s", where, secret, content)
		}
	}
}

func (f *tokenFixture) command(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()

	code, output := runCommand(t, env, args...)
	f.outputs = append(f.outputs, output)

	return code, output
}

func (f *tokenFixture) mustRun(t *testing.T, args ...string) string {
	t.Helper()

	code, output := f.command(t, f.env, args...)
	if code != 0 {
		t.Fatalf("%v: want exit 0, got %d: %s", args, code, output)
	}

	return output
}

// post returns the raw body beside the decoded one: two refusals are compared
// byte for byte.
func (f *tokenFixture) post(t *testing.T, form url.Values) (*http.Response, tokenResponse, string) {
	t.Helper()

	resp, err := f.server.client.PostForm(f.server.url(tokenPath), form)
	if err != nil {
		t.Fatalf("requesting tokens: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the token response: %v", err)
	}

	var decoded tokenResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding the token response: %v: %s", err, raw)
	}

	for _, secret := range []string{decoded.AccessToken, decoded.RefreshToken} {
		if secret != "" {
			f.secrets = append(f.secrets, secret)
		}
	}

	return resp, decoded, string(raw)
}

func (f *tokenFixture) login(t *testing.T) tokenResponse {
	t.Helper()

	resp, tokens, body := f.post(t, passwordForm(tokenEmail, identityPassword))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password grant: want 200, got %d: %s", resp.StatusCode, body)
	}

	return tokens
}

func (f *tokenFixture) refresh(t *testing.T, refreshToken string) tokenResponse {
	t.Helper()

	resp, tokens, body := f.post(t, refreshForm(refreshToken))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: want 200, got %d: %s", resp.StatusCode, body)
	}

	return tokens
}

func (f *tokenFixture) assertRefused(t *testing.T, what string, form url.Values) string {
	t.Helper()

	resp, refused, body := f.post(t, form)
	if resp.StatusCode != http.StatusBadRequest || refused.Error != "invalid_grant" {
		t.Errorf("%s: want 400 invalid_grant, got %d: %s", what, resp.StatusCode, body)
	}

	return body
}

func passwordForm(username, password string) url.Values {
	return url.Values{"grant_type": {"password"}, "username": {username}, "password": {password}}
}

func refreshForm(refreshToken string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
}

// sessionRows maps a session id to its STATUS column and returns the ids in the
// order they were printed.
func (f *tokenFixture) sessionRows(t *testing.T) (map[string]string, []string) {
	t.Helper()

	output := f.mustRun(t, "session", "list", "master", tokenLogin)

	statuses := map[string]string{}

	var order []string

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)

		// The query log lands in the same stream.
		if strings.HasPrefix(line, "{") || len(fields) < 2 || fields[0] == "ID" {
			continue
		}

		statuses[fields[0]] = fields[1]
		order = append(order, fields[0])
	}

	return statuses, order
}

func (e schemaEndpoint) endReasonOf(t *testing.T, sessionID string) string {
	t.Helper()

	var reason sql.NullString

	err := e.open(t).SQL.QueryRowContext(context.Background(),
		`SELECT end_reason FROM sessions WHERE CAST(id AS text) = $1`, sessionID).Scan(&reason)
	if err != nil {
		t.Fatalf("reading the session %s: %v", sessionID, err)
	}

	return reason.String
}

func (e schemaEndpoint) refreshTokenHashes(t *testing.T, sessionID string) []string {
	t.Helper()

	rows, err := e.open(t).SQL.QueryContext(context.Background(),
		`SELECT token_hash FROM refresh_tokens WHERE CAST(session_id AS text) = $1`, sessionID)
	if err != nil {
		t.Fatalf("reading the refresh tokens of %s: %v", sessionID, err)
	}
	defer rows.Close()

	var hashes []string

	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scanning a refresh token: %v", err)
		}

		hashes = append(hashes, hash)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("reading the refresh tokens of %s: %v", sessionID, err)
	}

	return hashes
}

// jwksKeys builds the lookup a third party would: from the published document
// alone, with nothing taken from the server's side.
func jwksKeys(t *testing.T, body string) token.KeyFunc {
	t.Helper()

	var set struct {
		Keys []key.JWK `json:"keys"`
	}

	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("decoding the jwks: %v: %s", err, body)
	}

	return func(kid, alg string) (crypto.PublicKey, error) {
		for _, published := range set.Keys {
			if published.Kid != kid || published.Alg != alg || published.Kty != "RSA" {
				continue
			}

			modulus, err := base64.RawURLEncoding.DecodeString(published.N)
			if err != nil {
				return nil, err
			}

			exponent, err := base64.RawURLEncoding.DecodeString(published.E)
			if err != nil {
				return nil, err
			}

			return &rsa.PublicKey{
				N: new(big.Int).SetBytes(modulus),
				E: int(new(big.Int).SetBytes(exponent).Int64()),
			}, nil
		}

		return nil, token.ErrKeyUnknown
	}
}

func joseHeader(t *testing.T, compact string) (alg, kid string) {
	t.Helper()

	segment, _, _ := strings.Cut(compact, ".")

	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decoding the token header: %v", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}

	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatalf("decoding the token header: %v: %s", err, raw)
	}

	return header.Alg, header.Kid
}

// The point of the phase: a token someone holding only the JWKS can verify.
func TestThePasswordGrantIssuesATokenTheJWKSVerifies(t *testing.T) {
	f := newTokenFixture(t)

	resp, tokens, body := f.post(t, passwordForm(tokenEmail, identityPassword))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if got := resp.Header.Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", got)
	}

	if tokens.TokenType != "Bearer" || tokens.ExpiresIn != 300 || tokens.RefreshExpiresIn <= 0 {
		t.Errorf("want a bearer token living 300s beside a refresh token, got %s", body)
	}

	status, jwks := f.server.get(t, jwksPath)
	if status != http.StatusOK {
		t.Fatalf("jwks: want 200, got %d: %s", status, jwks)
	}

	alg, kid := joseHeader(t, tokens.AccessToken)

	if alg != "RS256" {
		t.Errorf("alg = %q, want RS256", alg)
	}

	if kid != rsaKID(t, jwks) {
		t.Errorf("kid = %q, want the published RS256 key %q", kid, rsaKID(t, jwks))
	}

	claims, err := token.Verify(tokens.AccessToken, jwksKeys(t, jwks), time.Now())
	if err != nil {
		t.Fatalf("verifying the access token against the jwks: %v", err)
	}

	if claims.Issuer != f.issuer {
		t.Errorf("iss = %q, want %q", claims.Issuer, f.issuer)
	}

	if claims.Subject != f.identityID {
		t.Errorf("sub = %q, want the identity %q", claims.Subject, f.identityID)
	}

	if claims.SessionID != tokens.SessionState {
		t.Errorf("sid = %q, want session_state %q", claims.SessionID, tokens.SessionState)
	}

	if got := claims.ExpiresAt.Sub(claims.IssuedAt); got != 300*time.Second {
		t.Errorf("exp - iat = %s, want 300s", got)
	}

	hashes := f.endpoint.refreshTokenHashes(t, tokens.SessionState)
	if len(hashes) != 1 || !sha256Hex.MatchString(hashes[0]) {
		t.Fatalf("want one stored refresh token, as 64 hex characters, got %v", hashes)
	}

	if hashes[0] == tokens.RefreshToken || strings.Contains(tokens.RefreshToken, hashes[0]) {
		t.Error("the stored value must be a hash of the refresh token, not the token")
	}
}

func TestTheTokenEndpointRefusesGet(t *testing.T) {
	f := newTokenFixture(t)

	resp, err := f.server.client.Get(f.server.url(tokenPath))
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
}

// Identical bodies: a difference would tell a caller which logins exist.
func TestAWrongPasswordAndAnUnknownUserAreRefusedAlike(t *testing.T) {
	f := newTokenFixture(t)

	wrongPassword := f.assertRefused(t, "wrong password", passwordForm(tokenEmail, identityNextPassword))
	unknownUser := f.assertRefused(t, "unknown user", passwordForm("ghost@example.com", identityPassword))

	if wrongPassword != unknownUser {
		t.Errorf("want identical bodies, got %q and %q", wrongPassword, unknownUser)
	}
}

func TestRefreshRotatesAndAReplayedTokenEndsTheSession(t *testing.T) {
	f := newTokenFixture(t)

	first := f.login(t)
	second := f.refresh(t, first.RefreshToken)

	if second.RefreshToken == first.RefreshToken {
		t.Error("want a new refresh token on every refresh")
	}

	if second.SessionState != first.SessionState {
		t.Errorf("want the session kept, got %q then %q", first.SessionState, second.SessionState)
	}

	f.assertRefused(t, "the consumed refresh token", refreshForm(first.RefreshToken))

	// The legitimate holder loses the session too: nobody can tell which of
	// the two presenters stole the token.
	f.assertRefused(t, "the newest refresh token after a replay", refreshForm(second.RefreshToken))

	if got := f.endpoint.endReasonOf(t, first.SessionState); got != "reuse" {
		t.Errorf("end_reason = %q, want reuse", got)
	}

	if statuses, _ := f.sessionRows(t); statuses[first.SessionState] != "ended:reuse" {
		t.Errorf("want the session listed as ended:reuse, got %v", statuses)
	}
}

func TestSessionRevokeEndsOneSessionAndLeavesTheOthers(t *testing.T) {
	f := newTokenFixture(t)

	older := f.login(t)
	newer := f.login(t)

	statuses, order := f.sessionRows(t)

	if len(order) != 2 || order[0] != newer.SessionState || order[1] != older.SessionState {
		t.Fatalf("want both sessions, newest first, got %v", order)
	}

	if statuses[older.SessionState] != "live" || statuses[newer.SessionState] != "live" {
		t.Errorf("want both sessions live, got %v", statuses)
	}

	output := f.mustRun(t, "session", "revoke", "master", older.SessionState)
	if !strings.Contains(output, "session "+older.SessionState+" of realm master is now revoked") {
		t.Errorf("want the confirmation, got:\n%s", output)
	}

	f.assertRefused(t, "the revoked session's refresh token", refreshForm(older.RefreshToken))
	f.refresh(t, newer.RefreshToken)

	if got := f.endpoint.endReasonOf(t, older.SessionState); got != "revoked" {
		t.Errorf("end_reason = %q, want revoked", got)
	}

	if statuses, _ := f.sessionRows(t); statuses[older.SessionState] != "ended:revoked" ||
		statuses[newer.SessionState] != "live" {
		t.Errorf("want one session ended:revoked and the other live, got %v", statuses)
	}

	cases := map[string]struct {
		id   string
		code int
		want string
	}{
		"already ended": {
			id: older.SessionState, code: 1,
			want: `aegisd: session ` + older.SessionState + ` of realm "master" has already ended`,
		},
		"unknown": {
			id: "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", code: 1,
			want: `aegisd: no session 0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b in realm "master"`,
		},
		"not an id": {id: "nope", code: 2, want: `aegisd: "nope" is not a session id`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, output := f.command(t, f.env, "session", "revoke", "master", tc.id)
			if code != tc.code {
				t.Fatalf("want exit %d, got %d: %s", tc.code, code, output)
			}

			if !strings.Contains(output, tc.want) {
				t.Errorf("want %q, got %q", tc.want, output)
			}
		})
	}
}

func TestSessionRevokeAllEndsEverySessionOfTheIdentity(t *testing.T) {
	f := newTokenFixture(t)

	first := f.login(t)
	second := f.login(t)

	// By email here and by username in the list, so both forms of a login resolve.
	output := f.mustRun(t, "session", "revoke-all", "master", tokenEmail)
	if !strings.Contains(output, "revoked 2 sessions of "+tokenEmail+" in realm master") {
		t.Errorf("want two sessions reported, got:\n%s", output)
	}

	f.assertRefused(t, "the first session's refresh token", refreshForm(first.RefreshToken))
	f.assertRefused(t, "the second session's refresh token", refreshForm(second.RefreshToken))

	// Nothing left to end is not a failure.
	output = f.mustRun(t, "session", "revoke-all", "master", tokenEmail)
	if !strings.Contains(output, "revoked 0 sessions") {
		t.Errorf("want zero sessions reported, got:\n%s", output)
	}

	code, output := f.command(t, f.env, "session", "list", "master", "ghost")
	if code != 1 || !strings.Contains(output, `aegisd: no identity "ghost" in realm "master"`) {
		t.Errorf("want exit 1 naming the unknown login, got %d: %s", code, output)
	}
}

func TestDisablingAnIdentityStopsRefreshAndLogin(t *testing.T) {
	f := newTokenFixture(t)

	tokens := f.login(t)

	f.mustRun(t, "identity", "disable", "master", tokenLogin)

	f.assertRefused(t, "a refresh after the identity was disabled", refreshForm(tokens.RefreshToken))

	disabled := f.assertRefused(t, "a login after the identity was disabled", passwordForm(tokenEmail, identityPassword))
	wrongPassword := f.assertRefused(t, "a wrong password", passwordForm(tokenEmail, identityNextPassword))

	// A disabled identity must not be told apart from a wrong password.
	if disabled != wrongPassword {
		t.Errorf("want identical bodies, got %q and %q", disabled, wrongPassword)
	}
}
