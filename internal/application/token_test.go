package application

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/handler/oidc"
	"github.com/rootless-dev/aegis/internal/http/middleware"
	"github.com/rootless-dev/aegis/internal/http/response"
)

const (
	tokenPath = "/realms/master" + oidc.TokenPath

	testEmail    = "ana@example.com"
	testPassword = "correct-horse-battery-staple"
)

// newWiredApplication goes through New, on SQLite: newTestApplication has no
// services, and the token endpoint is nothing without them.
func newWiredApplication(t *testing.T) *Application {
	t.Helper()

	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Crypto.MasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Normalize()
	cfg.Database.Path = filepath.Join(t.TempDir(), "aegis.db")

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("assembling the application: %v", err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	return app
}

func tokenRequest(slug string, form url.Values) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost, "/realms/"+slug+oidc.TokenPath, strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return request
}

func passwordGrant(username, password string) url.Values {
	return url.Values{"grant_type": {"password"}, "username": {username}, "password": {password}}
}

func errorCodeOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("want a json body, got %q: %s", got, recorder.Body.String())
	}

	var body struct {
		Error string `json:"error"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the error body: %v: %s", err, recorder.Body.String())
	}

	return body.Error
}

func TestTokenRouteRefusesOtherMethodsInJSON(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(method, tokenPath, nil))

		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status = %d, want 405", method, recorder.Code)
		}

		if got := recorder.Header().Get("Allow"); got != http.MethodPost {
			t.Errorf("%s: Allow = %q, want POST", method, got)
		}

		// Without the mounted branch this is the page surface's HTML 405.
		if got := errorCodeOf(t, recorder); got != response.ErrorInvalidRequest {
			t.Errorf("%s: error = %q, want %s", method, got, response.ErrorInvalidRequest)
		}

		if got := recorder.Header().Get(middleware.ContentSecurityPolicyHeader); got != "" {
			t.Errorf("%s: the 405 must not carry the page surface's policy, got %q", method, got)
		}
	}
}

func TestPostToAWrongPathUnderRealmsAnswersJSON(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	request := tokenRequest("master", passwordGrant(testEmail, testPassword))
	request.URL.Path = "/realms/master/protocol/openid-connect/tokens"

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	if got := errorCodeOf(t, recorder); got != response.ErrorInvalidRequest {
		t.Errorf("error = %q, want %s", got, response.ErrorInvalidRequest)
	}
}

func TestTokenRequestToAnUnknownRealmAnswers404(t *testing.T) {
	app := newWiredApplication(t)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, tokenRequest("nope", passwordGrant(testEmail, testPassword)))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}

	if got := errorCodeOf(t, recorder); got != response.ErrorInvalidRequest {
		t.Errorf("error = %q, want %s", got, response.ErrorInvalidRequest)
	}
}

// The handler's own tests run it against a stub issuer; this is the one place
// the route, the session service and the key service are proven to be joined.
func TestTokenRouteIssuesTokensThroughTheAssembledServices(t *testing.T) {
	app := newWiredApplication(t)

	master, err := app.realms.Resolve(t.Context(), "master")
	if err != nil {
		t.Fatalf("resolving the master realm: %v", err)
	}

	if _, err := app.identities.CreateWithPassword(
		t.Context(), master.ID(), testEmail, "", testPassword, false,
	); err != nil {
		t.Fatalf("creating the identity: %v", err)
	}

	cases := map[string]struct {
		password string
		status   int
	}{
		"issued":  {password: testPassword, status: http.StatusOK},
		"refused": {password: "not-the-password", status: http.StatusBadRequest},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			app.router.ServeHTTP(recorder, tokenRequest("master", passwordGrant(testEmail, tc.password)))

			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}

			assertTokenResponseHeaders(t, recorder.Header())

			if tc.status != http.StatusOK {
				if got := errorCodeOf(t, recorder); got != response.ErrorInvalidGrant {
					t.Errorf("error = %q, want %s", got, response.ErrorInvalidGrant)
				}

				return
			}

			var issued struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				TokenType    string `json:"token_type"`
			}

			if err := json.Unmarshal(recorder.Body.Bytes(), &issued); err != nil {
				t.Fatalf("decoding the tokens: %v: %s", err, recorder.Body.String())
			}

			if issued.AccessToken == "" || issued.RefreshToken == "" || issued.TokenType != "Bearer" {
				t.Errorf("want a bearer pair, got %s", recorder.Body.String())
			}
		})
	}
}

func assertTokenResponseHeaders(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if got := header.Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", got)
	}

	if got := header.Get(middleware.ContentTypeOptionsHeader); got != "nosniff" {
		t.Errorf("%s = %q, want nosniff", middleware.ContentTypeOptionsHeader, got)
	}

	for _, name := range []string{
		middleware.ContentSecurityPolicyHeader,
		middleware.ReferrerPolicyHeader,
		middleware.FrameOptionsHeader,
	} {
		if got := header.Get(name); got != "" {
			t.Errorf("%s = %q, want the page surface to own it", name, got)
		}
	}
}
