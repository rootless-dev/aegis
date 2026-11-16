package oidc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/handler/oidc"
	"github.com/rootless-dev/aegis/internal/http/middleware"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	testUsername        = "alice@example.com"
	testPassword        = "correct-horse-battery"
	testRefreshToken    = "presented-refresh-token"
	issuedAccessToken   = "issued.access.token"
	issuedRefreshToken  = "issued-refresh-token"
	formContentType     = "application/x-www-form-urlencoded"
	jsonContentType     = "application/json;charset=UTF-8"
	httptestClientIP    = "192.0.2.1"
	testUserAgent       = "aegis-test/1.0"
	passwordGrantBody   = "grant_type=password&username=alice%40example.com&password=" + testPassword
	refreshGrantBody    = "grant_type=refresh_token&refresh_token=" + testRefreshToken
	credentialsRefused  = `{"error":"invalid_grant","error_description":"invalid user credentials"}`
	refreshTokenRefused = `{"error":"invalid_grant","error_description":"the refresh token is not valid"}`
)

var testSessionID = uuid.MustParse("0192f0c8-7a3b-7c1d-9e2f-3a4b5c6d7e8f")

func TestTokenPasswordGrantIssuesTokens(t *testing.T) {
	issuer := newIssuer()

	recorder, _ := exchange(t, issuer, activeRealm(), formRequest(passwordGrantBody+"&scope=openid"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", recorder.Code, recorder.Body)
	}

	if got := recorder.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("want json, got %q", got)
	}

	want := `{"access_token":"issued.access.token","token_type":"Bearer","expires_in":300,` +
		`"refresh_token":"issued-refresh-token","refresh_expires_in":1800,` +
		`"session_state":"0192f0c8-7a3b-7c1d-9e2f-3a4b5c6d7e8f"}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("want %s, got %s", want, got)
	}

	if issuer.logins != 1 || issuer.refreshes != 0 {
		t.Fatalf("want one login and no refresh, got %d and %d", issuer.logins, issuer.refreshes)
	}

	if issuer.login != testUsername || issuer.password != testPassword {
		t.Errorf("want the submitted credentials, got %q and %q", issuer.login, issuer.password)
	}

	if issuer.realm != "acme" {
		t.Errorf("want the resolved realm, got %q", issuer.realm)
	}
}

func TestTokenRefreshGrantIssuesTokens(t *testing.T) {
	issuer := newIssuer()

	recorder, _ := exchange(t, issuer, activeRealm(), formRequest(refreshGrantBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", recorder.Code, recorder.Body)
	}

	want := `{"access_token":"issued.access.token","token_type":"Bearer","expires_in":300,` +
		`"refresh_token":"issued-refresh-token","refresh_expires_in":1800,` +
		`"session_state":"0192f0c8-7a3b-7c1d-9e2f-3a4b5c6d7e8f"}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("want %s, got %s", want, got)
	}

	if issuer.refreshes != 1 || issuer.logins != 0 {
		t.Fatalf("want one refresh and no login, got %d and %d", issuer.refreshes, issuer.logins)
	}

	if issuer.refreshToken != testRefreshToken {
		t.Errorf("want the submitted refresh token, got %q", issuer.refreshToken)
	}
}

func TestTokenLifetimesAreWholeSecondsNeverNegative(t *testing.T) {
	issuer := newIssuer()
	issuer.tokens.AccessTokenTTL = 299*time.Second + 999*time.Millisecond
	issuer.tokens.RefreshTokenTTL = -time.Minute

	recorder, _ := exchange(t, issuer, activeRealm(), formRequest(passwordGrantBody))

	var body struct {
		ExpiresIn        int64 `json:"expires_in"`
		RefreshExpiresIn int64 `json:"refresh_expires_in"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if body.ExpiresIn != 299 || body.RefreshExpiresIn != 0 {
		t.Errorf("want 299 and 0, got %d and %d", body.ExpiresIn, body.RefreshExpiresIn)
	}
}

func TestTokenHandsTheClientToTheService(t *testing.T) {
	issuer := newIssuer()

	for _, body := range []string{passwordGrantBody, refreshGrantBody} {
		request := formRequest(body)
		request.Header.Set("User-Agent", testUserAgent)

		exchange(t, issuer, activeRealm(), request)

		want := service.ClientInfo{IP: httptestClientIP, UserAgent: testUserAgent}
		if issuer.client != want {
			t.Errorf("want %+v, got %+v", want, issuer.client)
		}

		issuer.client = service.ClientInfo{}
	}
}

func TestTokenAcceptsContentTypeParameters(t *testing.T) {
	for _, contentType := range []string{
		"application/x-www-form-urlencoded; charset=UTF-8",
		"application/x-www-form-urlencoded;charset=utf-8",
		"Application/X-WWW-Form-Urlencoded",
	} {
		request := formRequest(passwordGrantBody)
		request.Header.Set("Content-Type", contentType)

		if recorder, _ := exchange(t, newIssuer(), activeRealm(), request); recorder.Code != http.StatusOK {
			t.Errorf("%q: want 200, got %d", contentType, recorder.Code)
		}
	}
}

func TestTokenRefusesAnyOtherContentType(t *testing.T) {
	for _, contentType := range []string{
		"",
		"application/json",
		"text/plain",
		"multipart/form-data; boundary=x",
		"application/x-www-form-urlencoded-not",
	} {
		issuer := newIssuer()
		request := formRequest(passwordGrantBody)

		if contentType == "" {
			request.Header.Del("Content-Type")
		} else {
			request.Header.Set("Content-Type", contentType)
		}

		recorder, _ := exchange(t, issuer, activeRealm(), request)

		wantError(t, recorder, http.StatusBadRequest, "invalid_request")
		wantNeverCalled(t, issuer)
	}
}

func TestTokenRefusesABodyOverTheLimit(t *testing.T) {
	issuer := newIssuer()
	body := passwordGrantBody + "&scope=" + strings.Repeat("a", 16<<10)

	recorder, _ := exchange(t, issuer, activeRealm(), formRequest(body))

	wantError(t, recorder, http.StatusBadRequest, "invalid_request")
	wantNeverCalled(t, issuer)
}

func TestTokenRefusesAMalformedRequest(t *testing.T) {
	cases := map[string]string{
		"unparseable body":         "grant_type=password&username=%zz&password=" + testPassword,
		"empty body":               "",
		"no grant type":            "username=alice&password=" + testPassword,
		"empty grant type":         "grant_type=&username=alice&password=" + testPassword,
		"repeated grant type":      passwordGrantBody + "&grant_type=password",
		"repeated password":        passwordGrantBody + "&password=" + testPassword,
		"repeated ignored param":   passwordGrantBody + "&scope=openid&scope=profile",
		"repeated, one empty":      passwordGrantBody + "&username=",
		"password without user":    "grant_type=password&password=" + testPassword,
		"password with empty user": "grant_type=password&username=&password=" + testPassword,
		"user without password":    "grant_type=password&username=alice",
		"user with empty password": "grant_type=password&username=alice&password=",
		"refresh without a token":  "grant_type=refresh_token",
		"refresh with empty token": "grant_type=refresh_token&refresh_token=",
		"refresh with credentials": "grant_type=refresh_token&username=alice&password=" + testPassword,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			issuer := newIssuer()

			recorder, _ := exchange(t, issuer, activeRealm(), formRequest(body))

			wantError(t, recorder, http.StatusBadRequest, "invalid_request")
			wantNeverCalled(t, issuer)
		})
	}
}

func TestTokenTreatsAnEmptyParameterAsOmitted(t *testing.T) {
	recorder, _ := exchange(t, newIssuer(), activeRealm(), formRequest(passwordGrantBody+"&scope=&refresh_token="))

	if recorder.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", recorder.Code, recorder.Body)
	}
}

func TestTokenIgnoresTheQueryString(t *testing.T) {
	query := "?username=alice%40example.com&password=" + testPassword

	cases := map[string]*http.Request{
		"everything in the url":  postRequest("?grant_type=password&"+query[1:], ""),
		"credentials in the url": postRequest(query, "grant_type=password"),
		"refresh token in the url": postRequest(
			"?refresh_token="+testRefreshToken, "grant_type=refresh_token",
		),
	}

	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			issuer := newIssuer()

			recorder, _ := exchange(t, issuer, activeRealm(), request)

			wantError(t, recorder, http.StatusBadRequest, "invalid_request")
			wantNeverCalled(t, issuer)
		})
	}
}

func TestTokenIsNotConfusedByAMalformedQueryString(t *testing.T) {
	recorder, _ := exchange(t, newIssuer(), activeRealm(), postRequest("?a=%zz;b", passwordGrantBody))

	if recorder.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", recorder.Code, recorder.Body)
	}
}

func TestTokenRefusesAnUnsupportedGrantType(t *testing.T) {
	for _, grant := range []string{"authorization_code", "client_credentials", "PASSWORD", "nonsense"} {
		issuer := newIssuer()
		body := "grant_type=" + grant + "&username=alice&password=" + testPassword

		recorder, _ := exchange(t, issuer, activeRealm(), formRequest(body))

		wantError(t, recorder, http.StatusBadRequest, "unsupported_grant_type")
		wantNeverCalled(t, issuer)
	}
}

func TestTokenMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		err    error
		status int
		code   string
	}{
		{"mismatch", passwordGrantBody, credential.ErrMismatch, 400, "invalid_grant"},
		{"wrapped mismatch", passwordGrantBody, fmt.Errorf("login: %w", credential.ErrMismatch), 400, "invalid_grant"},
		{"disabled identity", passwordGrantBody, identity.ErrDisabled, 400, "invalid_grant"},
		{"password grant disabled", passwordGrantBody, service.ErrPasswordGrantDisabled, 400, "unauthorized_client"},
		{"disabled realm on login", passwordGrantBody, service.ErrRealmDisabled, 400, "invalid_grant"},
		{"disabled realm on refresh", refreshGrantBody, service.ErrRealmDisabled, 400, "invalid_grant"},
		{"token not found", refreshGrantBody, session.ErrTokenNotFound, 400, "invalid_grant"},
		{"token expired", refreshGrantBody, session.ErrTokenExpired, 400, "invalid_grant"},
		{"session ended", refreshGrantBody, session.ErrEnded, 400, "invalid_grant"},
		{"session expired", refreshGrantBody, session.ErrExpired, 400, "invalid_grant"},
		{"reuse", refreshGrantBody, session.ErrReuseDetected, 400, "invalid_grant"},
		{"wrapped reuse", refreshGrantBody, fmt.Errorf("refresh: %w", session.ErrReuseDetected), 400, "invalid_grant"},
		{"unexpected on login", passwordGrantBody, errors.New("database is gone"), 500, "server_error"},
		{"unexpected on refresh", refreshGrantBody, session.ErrTokenUsed, 500, "server_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder, _ := exchange(t, &fakeIssuer{err: tc.err}, activeRealm(), formRequest(tc.body))

			wantError(t, recorder, tc.status, tc.code)
		})
	}
}

func TestTokenDoesNotTellAWrongPasswordFromADisabledIdentity(t *testing.T) {
	mismatch, _ := exchange(t, &fakeIssuer{err: credential.ErrMismatch}, activeRealm(), formRequest(passwordGrantBody))
	disabled, _ := exchange(t, &fakeIssuer{err: identity.ErrDisabled}, activeRealm(), formRequest(passwordGrantBody))

	if mismatch.Body.String() != credentialsRefused {
		t.Errorf("want %s, got %s", credentialsRefused, mismatch.Body)
	}

	wantSameResponse(t, mismatch, disabled)
}

func TestTokenDoesNotTellRefreshFailuresApart(t *testing.T) {
	failures := []error{
		session.ErrTokenNotFound, session.ErrTokenExpired, session.ErrEnded,
		session.ErrExpired, session.ErrReuseDetected, identity.ErrDisabled,
	}

	first, _ := exchange(t, &fakeIssuer{err: failures[0]}, activeRealm(), formRequest(refreshGrantBody))

	if first.Body.String() != refreshTokenRefused {
		t.Errorf("want %s, got %s", refreshTokenRefused, first.Body)
	}

	for _, failure := range failures[1:] {
		other, _ := exchange(t, &fakeIssuer{err: failure}, activeRealm(), formRequest(refreshGrantBody))

		wantSameResponse(t, first, other)
	}
}

func TestTokenLogsAFailedLoginWithoutTheUsername(t *testing.T) {
	_, logs := exchange(t, &fakeIssuer{err: credential.ErrMismatch}, activeRealm(), formRequest(passwordGrantBody))

	for _, want := range []string{`"level":"info"`, `"realm":"acme"`, `"client_ip":"` + httptestClientIP + `"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("want %s in the log, got %s", want, logs)
		}
	}

	// exchange already fails on the full username; the local part alone must
	// not be there either.
	if strings.Contains(logs, "alice") {
		t.Errorf("want no username in the log, got %s", logs)
	}
}

func TestTokenWarnsOnRefreshTokenReuse(t *testing.T) {
	_, logs := exchange(t, &fakeIssuer{err: session.ErrReuseDetected}, activeRealm(), formRequest(refreshGrantBody))

	for _, want := range []string{`"level":"warn"`, `"realm":"acme"`, "reuse"} {
		if !strings.Contains(logs, want) {
			t.Errorf("want %s in the log, got %s", want, logs)
		}
	}
}

func TestTokenDoesNotWarnOnAnOrdinaryRefreshFailure(t *testing.T) {
	_, logs := exchange(t, &fakeIssuer{err: session.ErrTokenExpired}, activeRealm(), formRequest(refreshGrantBody))

	if strings.Contains(logs, `"level":"warn"`) {
		t.Errorf("want no warning, got %s", logs)
	}
}

func TestTokenLogsAnUnexpectedErrorWithTheRealm(t *testing.T) {
	_, logs := exchange(t, &fakeIssuer{err: errors.New("database is gone")}, activeRealm(), formRequest(passwordGrantBody))

	for _, want := range []string{`"level":"error"`, `"realm":"acme"`, "database is gone"} {
		if !strings.Contains(logs, want) {
			t.Errorf("want %s in the log, got %s", want, logs)
		}
	}
}

func TestTokenIssuesAndWarnsWhenThePasswordMustChange(t *testing.T) {
	issuer := newIssuer()
	issuer.tokens.MustChangePassword = true

	recorder, logs := exchange(t, issuer, activeRealm(), formRequest(passwordGrantBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", recorder.Code, recorder.Body)
	}

	for _, want := range []string{`"level":"warn"`, `"realm":"acme"`, testSessionID.String()} {
		if !strings.Contains(logs, want) {
			t.Errorf("want %s in the log, got %s", want, logs)
		}
	}
}

func TestTokenIsQuietOnAnOrdinarySuccess(t *testing.T) {
	_, logs := exchange(t, newIssuer(), activeRealm(), formRequest(passwordGrantBody))

	if logs != "" {
		t.Errorf("want nothing logged, got %s", logs)
	}
}

func TestTokenRefusesAnUnknownOrArchivedRealm(t *testing.T) {
	for _, err := range []error{realm.ErrNotFound, realm.ErrNotAvailable} {
		issuer := newIssuer()

		recorder, _ := exchange(t, issuer, fakeResolver{err: err}, formRequest(passwordGrantBody))

		wantError(t, recorder, http.StatusNotFound, "invalid_request")
		wantNeverCalled(t, issuer)
	}
}

func TestTokenAnswersAServerErrorWhenTheRealmCannotBeResolved(t *testing.T) {
	issuer := newIssuer()

	recorder, _ := exchange(t, issuer, fakeResolver{err: errors.New("database is gone")}, formRequest(passwordGrantBody))

	wantError(t, recorder, http.StatusInternalServerError, "server_error")
	wantNeverCalled(t, issuer)
}

func TestMountRegistersTheTokenEndpointForPostOnly(t *testing.T) {
	router := &recordingRouter{}

	oidc.New(activeRealm(), fakeKeys{}, newIssuer(), func(*http.Request) string { return "acme" }, testLogger()).
		Mount(router)

	var methods []string

	for _, route := range router.routes {
		if method, pattern, _ := strings.Cut(route, " "); pattern == "/{realm}"+oidc.TokenPath {
			methods = append(methods, method)
		}
	}

	if len(methods) != 1 || methods[0] != http.MethodPost {
		t.Errorf("want POST alone on the token path, got %v", methods)
	}
}

// --- fakes and helpers ---

type fakeIssuer struct {
	tokens *service.Tokens
	err    error

	logins, refreshes             int
	realm                         string
	login, password, refreshToken string
	client                        service.ClientInfo
}

func newIssuer() *fakeIssuer {
	return &fakeIssuer{tokens: &service.Tokens{
		AccessToken:     issuedAccessToken,
		AccessTokenTTL:  5 * time.Minute,
		RefreshToken:    issuedRefreshToken,
		RefreshTokenTTL: 30 * time.Minute,
		SessionID:       testSessionID,
	}}
}

func (f *fakeIssuer) Login(
	_ context.Context, r *realm.Realm, login, password string, client service.ClientInfo,
) (*service.Tokens, error) {
	f.logins++
	f.realm, f.login, f.password, f.client = r.Slug(), login, password, client

	return f.tokens, f.err
}

func (f *fakeIssuer) Refresh(
	_ context.Context, r *realm.Realm, refreshToken string, client service.ClientInfo,
) (*service.Tokens, error) {
	f.refreshes++
	f.realm, f.refreshToken, f.client = r.Slug(), refreshToken, client

	return f.tokens, f.err
}

type recordingRouter struct{ routes []string }

func (r *recordingRouter) Get(pattern string, _ http.HandlerFunc) {
	r.routes = append(r.routes, http.MethodGet+" "+pattern)
}

func (r *recordingRouter) Head(pattern string, _ http.HandlerFunc) {
	r.routes = append(r.routes, http.MethodHead+" "+pattern)
}

func (r *recordingRouter) Post(pattern string, _ http.HandlerFunc) {
	r.routes = append(r.routes, http.MethodPost+" "+pattern)
}

func activeRealm() fakeResolver {
	return fakeResolver{status: realm.StatusActive}
}

func formRequest(body string) *http.Request {
	return postRequest("", body)
}

func postRequest(query, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/realms/acme"+oidc.TokenPath+query, strings.NewReader(body))
	request.Header.Set("Content-Type", formContentType)

	return request
}

// exchange serves one token request behind the proxy middleware, which is what
// puts the client address in the context, and returns the response and what
// was logged. It holds every response and every log to the rules no test may
// break: both cache headers, no secret logged, nothing submitted echoed back.
func exchange(
	t *testing.T, issuer *fakeIssuer, resolver fakeResolver, request *http.Request,
) (*httptest.ResponseRecorder, string) {
	t.Helper()

	var logs bytes.Buffer

	handler := oidc.New(
		resolver, fakeKeys{}, issuer,
		func(*http.Request) string { return "acme" },
		&log.Logger{Level: log.TraceLevel, Writer: log.IOWriter{Writer: &logs}},
	)

	recorder := httptest.NewRecorder()
	middleware.Proxy(middleware.ProxyOptions{})(http.HandlerFunc(handler.Token)).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("want Cache-Control: no-store, got %q", got)
	}

	if got := recorder.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("want Pragma: no-cache, got %q", got)
	}

	submitted := []string{testUsername, url.QueryEscape(testUsername), testPassword, testRefreshToken}

	for _, secret := range append(submitted, issuedAccessToken, issuedRefreshToken) {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("want %q never logged, got %s", secret, logs.String())
		}
	}

	if recorder.Code != http.StatusOK {
		for _, value := range submitted {
			if strings.Contains(recorder.Body.String(), value) {
				t.Errorf("want %q not echoed in an error, got %s", value, recorder.Body)
			}
		}
	}

	return recorder, logs.String()
}

func wantError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	if recorder.Code != status {
		t.Errorf("want %d, got %d: %s", status, recorder.Code, recorder.Body)
	}

	if got := recorder.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("want a json error, got %q", got)
	}

	var body struct {
		Code string `json:"error"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", recorder.Body, err)
	}

	if body.Code != code {
		t.Errorf("want %s, got %s", code, recorder.Body)
	}
}

func wantNeverCalled(t *testing.T, issuer *fakeIssuer) {
	t.Helper()

	if issuer.logins != 0 || issuer.refreshes != 0 {
		t.Errorf("want the service never called, got %d logins and %d refreshes", issuer.logins, issuer.refreshes)
	}
}

func wantSameResponse(t *testing.T, a, b *httptest.ResponseRecorder) {
	t.Helper()

	if a.Code != b.Code {
		t.Errorf("want the same status, got %d and %d", a.Code, b.Code)
	}

	if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Errorf("want the same body, got %s and %s", a.Body, b.Body)
	}

	if fmt.Sprint(a.Header()) != fmt.Sprint(b.Header()) {
		t.Errorf("want the same headers, got %v and %v", a.Header(), b.Header())
	}
}
