package assets_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rootless-dev/aegis/internal/http/assets"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"css/app.css": &fstest.MapFile{Data: []byte("body{color:red}")},
		"favicon.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}
}

func newServer(t *testing.T, files fstest.MapFS) *assets.Server {
	t.Helper()

	server, err := assets.New(files)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return server
}

// frame-ancestors is in here on purpose: it is one of the directives that does
// not fall back to default-src, and the asset routes bypass SecurityHeaders.
const wantAssetPolicy = "default-src 'none'; frame-ancestors 'none'"

func TestURLIsStableForTheSameContent(t *testing.T) {
	first := newServer(t, testFS())
	second := newServer(t, testFS())

	firstURL, err := first.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	secondURL, err := second.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	if firstURL != secondURL {
		t.Errorf("same content produced different urls: %q and %q", firstURL, secondURL)
	}

	if !strings.HasPrefix(firstURL, "/assets/") || !strings.HasSuffix(firstURL, "/css/app.css") {
		t.Errorf("unexpected url shape: %q", firstURL)
	}
}

func TestURLChangesWithContent(t *testing.T) {
	original := newServer(t, testFS())

	changed := testFS()
	changed["css/app.css"] = &fstest.MapFile{Data: []byte("body{color:blue}")}

	before, _ := original.URL(assets.Stylesheet)
	after, _ := newServer(t, changed).URL(assets.Stylesheet)

	if before == after {
		t.Error("changed content must produce a different url, or a deploy cannot invalidate the cache")
	}
}

func TestURLRejectsAnUnknownPath(t *testing.T) {
	server := newServer(t, testFS())

	if _, err := server.URL("css/missing.css"); err == nil {
		t.Error("an unknown logical path must fail rather than emit a broken url")
	}
}

func TestVerifyReportsWhatIsMissing(t *testing.T) {
	files := testFS()
	delete(files, "css/app.css")

	err := newServer(t, files).Verify(assets.Stylesheet)
	if err == nil {
		t.Fatal("Verify must fail when a required asset is absent")
	}

	if !strings.Contains(err.Error(), assets.Stylesheet) {
		t.Errorf("the error must name the missing asset, got %q", err)
	}
}

func TestVerifyPassesWhenEverythingIsPresent(t *testing.T) {
	if err := newServer(t, testFS()).Verify(assets.Stylesheet, assets.Favicon); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// router is the smallest thing satisfying assets.Router.
type router struct{ routes map[string]http.HandlerFunc }

func (r *router) Get(pattern string, handler http.HandlerFunc) {
	r.routes[pattern] = handler
}

func (r *router) Head(pattern string, handler http.HandlerFunc) {
	r.routes[pattern] = handler
}

func mounted(t *testing.T, server *assets.Server) *router {
	t.Helper()

	mux := &router{routes: map[string]http.HandlerFunc{}}
	server.Mount(mux)

	return mux
}

func TestServeReturnsTheFileWithImmutableCaching(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	url, _ := server.URL(assets.Stylesheet)

	recorder := httptest.NewRecorder()
	handler := mux.routes["/assets/*"]
	if handler == nil {
		t.Fatal("Mount did not register the asset route")
	}

	handler(recorder, httptest.NewRequest(http.MethodGet, url, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Body.String(); got != "body{color:red}" {
		t.Errorf("body = %q", got)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}

	if recorder.Header().Get("ETag") == "" {
		t.Error("ETag must be set, it is what makes If-None-Match work")
	}

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// Written out by hand: comparing the constant to itself would pass whatever
	// it said, including a directive dropped by a refactor.
	if got := recorder.Header().Get("Content-Security-Policy"); got != wantAssetPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantAssetPolicy)
	}
}

func TestServeRejectsAWrongHash(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	recorder := httptest.NewRecorder()
	mux.routes["/assets/*"](recorder, httptest.NewRequest(http.MethodGet, "/assets/deadbeef/css/app.css", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: without the check any string serves the file and immutable caching is a lie", recorder.Code)
	}
}

func TestServeAnswersNotModified(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	url, _ := server.URL(assets.Stylesheet)

	first := httptest.NewRecorder()
	mux.routes["/assets/*"](first, httptest.NewRequest(http.MethodGet, url, nil))

	request := httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))

	second := httptest.NewRecorder()
	mux.routes["/assets/*"](second, request)

	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", second.Code)
	}
}

func TestFaviconIsServedFromTheFixedPath(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	handler := mux.routes["/favicon.ico"]
	if handler == nil {
		t.Fatal("Mount did not register /favicon.ico; browsers request it whatever the document says")
	}

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	// Fixed path: a deploy cannot invalidate it, so it must not claim a year.
	if strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
		t.Error("the favicon path is fixed and must not be cached as immutable")
	}

	// This route answers image/svg+xml, and an SVG navigated to directly can
	// carry script.
	// Written out by hand: comparing the constant to itself would pass whatever
	// it said, including a directive dropped by a refactor.
	if got := recorder.Header().Get("Content-Security-Policy"); got != wantAssetPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantAssetPolicy)
	}
}

func TestFaviconIsServedAsSVG(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	recorder := httptest.NewRecorder()
	mux.routes["/favicon.ico"](recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	// The url says .ico, the bytes are an SVG, and only the content type tells
	// the browser which to believe.
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
}

func TestFaviconIsNotFoundWhenTheFileIsAbsent(t *testing.T) {
	files := testFS()
	delete(files, assets.Favicon)

	mux := mounted(t, newServer(t, files))

	recorder := httptest.NewRecorder()
	mux.routes["/favicon.ico"](recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", recorder.Code)
	}
}

func TestServeRejectsAPathWithoutALogicalName(t *testing.T) {
	mux := mounted(t, newServer(t, testFS()))

	recorder := httptest.NewRecorder()
	mux.routes["/assets/*"](recorder, httptest.NewRequest(http.MethodGet, "/assets/deadbeef", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", recorder.Code)
	}
}
