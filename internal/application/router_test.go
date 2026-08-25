package application

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/http/middleware"
	"github.com/rootless-dev/aegis/internal/infra/health"
)

func newTestApplication(t *testing.T, logs *bytes.Buffer) *Application {
	t.Helper()

	app := &Application{
		cfg: &configs.Application{
			AppName:   "aegis-test",
			Profile:   configs.ProfileProd,
			PublicURL: "https://aegis.test",
			HttpServer: &configs.HttpServer{
				Port: "7500", Host: "127.0.0.1",
				RequestTimeout: 10 * time.Second,
			},
			Graceful: &configs.Graceful{Timeout: 20 * time.Second},
			Health:   &configs.Health{CheckTimeout: time.Second, DrainDelay: time.Second},
			TLS:      &configs.TLS{Termination: configs.TerminationNone},
			Proxy:    &configs.Proxy{},
			HSTS:     &configs.HSTS{},
			CSP:      &configs.CSP{Enabled: true},
		},
		logger: &log.Logger{Writer: log.IOWriter{Writer: logs}},
	}

	if err := app.setGraceful(); err != nil {
		t.Fatalf("graceful: %v", err)
	}

	if err := app.setHealth(); err != nil {
		t.Fatalf("health: %v", err)
	}

	if err := app.setWeb(); err != nil {
		t.Fatalf("web: %v", err)
	}

	if err := app.setRouter(); err != nil {
		t.Fatalf("router: %v", err)
	}

	return app
}

func TestProbesAnswerAndStayOutOfTheRequestLog(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, recorder.Code)
		}
	}

	// The orchestrator polls these every few seconds per replica; logging them
	// would bury the real traffic.
	if strings.Contains(logs.String(), "http request") {
		t.Errorf("probes must not reach the request logger, got: %s", logs.String())
	}
}

func TestGroupedRoutesGoThroughTheFullChain(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	// Mounted the same way a domain surface would be, to exercise the chain the
	// group installs.
	app.surfaces.Get("/grouped", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/grouped", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", recorder.Code)
	}

	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("grouped routes should carry a request id")
	}

	if !strings.Contains(logs.String(), "http request") {
		t.Errorf("grouped routes should be logged, got: %s", logs.String())
	}
}

// A section nobody filled in must not be read as permission to serve pages
// without a policy.
func TestContentSecurityPolicyFailsClosed(t *testing.T) {
	cases := map[string]struct {
		csp  *configs.CSP
		want string
	}{
		"absent section":  {csp: nil, want: middleware.ContentSecurityPolicy},
		"enabled":         {csp: &configs.CSP{Enabled: true}, want: middleware.ContentSecurityPolicy},
		"disabled by ops": {csp: &configs.CSP{Enabled: false}, want: ""},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			app := &Application{cfg: &configs.Application{CSP: testCase.csp}}

			if got := app.contentSecurityPolicy(); got != testCase.want {
				t.Errorf("policy = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNewRejectsNilConfiguration(t *testing.T) {
	if _, err := New(nil); err != ErrConfigurationIsNil {
		t.Errorf("want ErrConfigurationIsNil, got %v", err)
	}
}

// fallbackMarker is a sentence only render.Fallback writes. The fallback
// carries the same headers a rendered page does, so a test stopping at the
// headers would pass against a service where every render failed.
const fallbackMarker = "The request could not be completed."

func TestLandingPageAnswersHTML(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if recorder.Header().Get("Content-Security-Policy") == "" {
		t.Error("the page surface must carry a content security policy")
	}

	body := recorder.Body.String()

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the landing route answered with the fallback document:\n%s", body)
	}

	// Content only the real landing page produces, through the real model.
	for _, want := range []string{"<title>Aegis</title>", "Identity for every tenant."} {
		if !strings.Contains(body, want) {
			t.Errorf("the landing page is missing %q:\n%s", want, body)
		}
	}

	// Resolved through the same fingerprint map the asset server serves from,
	// which is what proves the layout's `asset` call succeeded.
	stylesheet, err := app.assets.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("the stylesheet must be generated before this test: %v", err)
	}

	if !strings.Contains(body, `href="`+stylesheet+`"`) {
		t.Errorf("the page does not link the fingerprinted stylesheet %q:\n%s", stylesheet, body)
	}
}

func TestUnknownPathAnswersHTML(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	// An unregistered path is more likely a typo than a missing endpoint.
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want html", recorder.Header().Get("Content-Type"))
	}

	// Registered on the pages group, so a 404 still carries what the page
	// surface is defined to carry.
	if recorder.Header().Get("Content-Security-Policy") == "" {
		t.Error("the 404 must carry the page surface's content security policy")
	}

	body := recorder.Body.String()

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the 404 answered with the fallback document:\n%s", body)
	}

	// The rendered error page, not merely something with a 404 status on it.
	for _, want := range []string{">404<", "That page does not exist."} {
		if !strings.Contains(body, want) {
			t.Errorf("the error page is missing %q:\n%s", want, body)
		}
	}
}

func TestFaviconDoesNotRenderAPage(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	// Falling through to the HTML 404 would render a whole page for an icon.
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func TestProbesStillAnswerJSON(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("%s: Content-Type = %q, want json", path, got)
		}

		if recorder.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s: probes must not carry the page security headers", path)
		}
	}
}

// Walks the assembled router instead of listing known paths, so it also covers
// routes added later. It runs against the real router because health and assets
// declare their own Router interface, and testing those in isolation would pass
// while the application wired something else.
func TestEveryGetRouteAlsoAnswersHead(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	registered := map[string]map[string]bool{}

	err := chi.Walk(app.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if registered[route] == nil {
			registered[route] = map[string]bool{}
		}

		registered[route][method] = true

		return nil
	})
	if err != nil {
		t.Fatalf("walking the router: %v", err)
	}

	// A walk that found nothing would let every assertion below pass vacuously.
	if len(registered) == 0 {
		t.Fatal("the walk found no routes at all")
	}

	for route, methods := range registered {
		if methods[http.MethodGet] && !methods[http.MethodHead] {
			t.Errorf("%s is registered for GET but not for HEAD", route)
		}
	}
}

// The behaviour the walk only implies: the registration is there, and it
// answers. The probes prompted this — a monitor asking for headers alone used
// to get a 405, in HTML, since the page surface owns MethodNotAllowed.
func TestHeadIsAnsweredAcrossEverySurface(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	stylesheet, err := app.assets.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("the stylesheet must be generated before this test: %v", err)
	}

	for _, path := range []string{"/", health.LivenessPath, health.ReadinessPath, "/favicon.ico", stylesheet} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, path, nil))

		if recorder.Code != http.StatusOK {
			t.Errorf("HEAD %s: status = %d, want 200", path, recorder.Code)
		}
	}
}

// The two surfaces must recover differently, or the split was pointless.
func TestEachSurfaceRecoversInItsOwnFormat(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	app.pages.Get("/boom-html", func(http.ResponseWriter, *http.Request) { panic("boom") })
	app.surfaces.Get("/boom-json", func(http.ResponseWriter, *http.Request) { panic("boom") })

	html := httptest.NewRecorder()
	app.router.ServeHTTP(html, httptest.NewRequest(http.MethodGet, "/boom-html", nil))

	if html.Code != http.StatusInternalServerError {
		t.Fatalf("html surface: status = %d, want 500", html.Code)
	}

	if !strings.Contains(html.Header().Get("Content-Type"), "text/html") {
		t.Errorf("html surface answered %q; a browser must not get JSON", html.Header().Get("Content-Type"))
	}

	// The rendered error page, not the floor under it: the fallback would
	// produce the same status and content type.
	if body := html.Body.String(); !strings.Contains(body, ">500<") || strings.Contains(body, fallbackMarker) {
		t.Errorf("the panic was not answered by the error page:\n%s", body)
	}

	json := httptest.NewRecorder()
	app.router.ServeHTTP(json, httptest.NewRequest(http.MethodGet, "/boom-json", nil))

	if !strings.Contains(json.Header().Get("Content-Type"), "application/json") {
		t.Errorf("api surface answered %q; a client must not get HTML", json.Header().Get("Content-Type"))
	}
}

// The unit test on the handler cannot see this: the Allow header is put on by
// the router, from the methods the path actually accepts.
func TestMethodNotAllowedCarriesAllow(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}

	allow := recorder.Header().Get("Allow")

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if !strings.Contains(allow, method) {
			t.Errorf("Allow = %q, missing %s", allow, method)
		}
	}

	// The method that was refused must not be advertised as accepted.
	if strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow = %q, must not list the refused method", allow)
	}

	if !strings.Contains(recorder.Body.String(), "That method is not allowed here.") {
		t.Errorf("the 405 body is not the rendered page:\n%s", recorder.Body.String())
	}
}

// nosniff belongs to every response; the rest of the page headers belong only to
// the page surface.
func TestJSONResponsesCarryNosniffWithoutThePageHeaders(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if got := recorder.Header().Get(middleware.ContentTypeOptionsHeader); got != "nosniff" {
			t.Errorf("%s: %s = %q, want nosniff", path, middleware.ContentTypeOptionsHeader, got)
		}

		for _, header := range []string{
			middleware.ContentSecurityPolicyHeader,
			middleware.ReferrerPolicyHeader,
			middleware.FrameOptionsHeader,
		} {
			if got := recorder.Header().Get(header); got != "" {
				t.Errorf("%s: %s = %q, want the page surface to own it", path, header, got)
			}
		}
	}
}
